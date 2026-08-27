// Discovers connect procedures through grpc server reflection
package seed

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"
	"google.golang.org/grpc/reflection/grpc_reflection_v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

// Reflection procedure every connect era panel serves
const reflectionPath = "/grpc.reflection.v1.ServerReflection/ServerReflectionInfo"

// Cleartext http2 client for bidi streams
func h2cClient() *http.Client {
	return &http.Client{Transport: &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}}
}

// Pulls every service descriptor off the running panel
func DiscoverConnect(ctx context.Context, base string) (*Surface, error) {
	files, services, err := fetchDescriptors(ctx, base)
	if err != nil {
		return nil, err
	}
	surface := &Surface{Era: "connect"}
	shapes := &shapeCache{done: map[protoreflect.FullName]*Shape{}}
	for _, name := range services {
		if strings.HasPrefix(name, "grpc.") {
			continue
		}
		desc, err := files.FindDescriptorByName(protoreflect.FullName(name))
		if err != nil {
			return nil, fmt.Errorf("service %s: %w", name, err)
		}
		sd, ok := desc.(protoreflect.ServiceDescriptor)
		if !ok {
			continue
		}
		for i := 0; i < sd.Methods().Len(); i++ {
			md := sd.Methods().Get(i)
			if md.IsStreamingClient() || md.IsStreamingServer() {
				continue
			}
			surface.Ops = append(surface.Ops, &Operation{
				Name:   string(md.Name()),
				Method: http.MethodPost,
				Path:   "/" + string(sd.FullName()) + "/" + string(md.Name()),
				Input:  shapes.message(md.Input()),
				Output: shapes.message(md.Output()),
			})
		}
	}
	if len(surface.Ops) == 0 {
		return nil, fmt.Errorf("reflection listed no unary procedures")
	}
	return surface, nil
}

// Streams reflection requests until every file is known
func fetchDescriptors(ctx context.Context, base string) (*protoregistry.Files, []string, error) {
	client := connect.NewClient[grpc_reflection_v1.ServerReflectionRequest, grpc_reflection_v1.ServerReflectionResponse](
		h2cClient(), strings.TrimRight(base, "/")+reflectionPath)
	stream := client.CallBidiStream(ctx)
	defer stream.CloseRequest()

	ask := func(req *grpc_reflection_v1.ServerReflectionRequest) (*grpc_reflection_v1.ServerReflectionResponse, error) {
		if err := stream.Send(req); err != nil {
			return nil, err
		}
		resp, err := stream.Receive()
		if err != nil {
			return nil, err
		}
		if e := resp.GetErrorResponse(); e != nil {
			return nil, fmt.Errorf("reflection %d %s", e.GetErrorCode(), e.GetErrorMessage())
		}
		return resp, nil
	}

	resp, err := ask(&grpc_reflection_v1.ServerReflectionRequest{
		MessageRequest: &grpc_reflection_v1.ServerReflectionRequest_ListServices{ListServices: ""},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("list services: %w", err)
	}
	var services []string
	for _, svc := range resp.GetListServicesResponse().GetService() {
		services = append(services, svc.GetName())
	}

	known := map[string]*descriptorpb.FileDescriptorProto{}
	absorb := func(resp *grpc_reflection_v1.ServerReflectionResponse) error {
		for _, raw := range resp.GetFileDescriptorResponse().GetFileDescriptorProto() {
			fd := &descriptorpb.FileDescriptorProto{}
			if err := proto.Unmarshal(raw, fd); err != nil {
				return err
			}
			known[fd.GetName()] = fd
		}
		return nil
	}
	for _, name := range services {
		resp, err := ask(&grpc_reflection_v1.ServerReflectionRequest{
			MessageRequest: &grpc_reflection_v1.ServerReflectionRequest_FileContainingSymbol{FileContainingSymbol: name},
		})
		if err != nil {
			return nil, nil, fmt.Errorf("file containing %s: %w", name, err)
		}
		if err := absorb(resp); err != nil {
			return nil, nil, err
		}
	}
	// Dependencies missing from the replies are fetched by name
	// Annotation files a binary never registers stay placeholders
	unresolved := map[string]bool{}
	for {
		var missing []string
		for _, fd := range known {
			for _, dep := range fd.GetDependency() {
				if _, ok := known[dep]; !ok && !unresolved[dep] {
					missing = append(missing, dep)
				}
			}
		}
		if len(missing) == 0 {
			break
		}
		for _, name := range missing {
			resp, err := ask(&grpc_reflection_v1.ServerReflectionRequest{
				MessageRequest: &grpc_reflection_v1.ServerReflectionRequest_FileByFilename{FileByFilename: name},
			})
			if err != nil {
				unresolved[name] = true
				continue
			}
			if err := absorb(resp); err != nil {
				return nil, nil, err
			}
			if _, ok := known[name]; !ok {
				unresolved[name] = true
			}
		}
	}

	set := &descriptorpb.FileDescriptorSet{}
	for _, fd := range known {
		set.File = append(set.File, fd)
	}
	files, err := protodesc.FileOptions{AllowUnresolvable: true}.NewFiles(set)
	if err != nil {
		return nil, nil, fmt.Errorf("build descriptors: %w", err)
	}
	return files, services, nil
}

// Memoizes message shapes and breaks descriptor cycles
type shapeCache struct {
	done map[protoreflect.FullName]*Shape
}

// Shape of one message descriptor
func (c *shapeCache) message(md protoreflect.MessageDescriptor) *Shape {
	if s, ok := c.done[md.FullName()]; ok {
		return s
	}
	s := &Shape{Kind: KindMessage, Name: string(md.FullName())}
	c.done[md.FullName()] = s
	for i := 0; i < md.Fields().Len(); i++ {
		fd := md.Fields().Get(i)
		s.Fields = append(s.Fields, &Field{
			Name:     string(fd.Name()),
			Shape:    c.field(fd),
			Optional: fd.HasOptionalKeyword(),
		})
	}
	return s
}

// Shape of one field descriptor
func (c *shapeCache) field(fd protoreflect.FieldDescriptor) *Shape {
	switch {
	case fd.IsMap():
		return &Shape{Kind: KindMap, Key: c.scalar(fd.MapKey()), Elem: c.scalar(fd.MapValue())}
	case fd.IsList():
		return &Shape{Kind: KindList, Elem: c.scalar(fd)}
	}
	return c.scalar(fd)
}

// Shape of a single value ignoring cardinality
func (c *shapeCache) scalar(fd protoreflect.FieldDescriptor) *Shape {
	switch fd.Kind() {
	case protoreflect.StringKind:
		return &Shape{Kind: KindString}
	case protoreflect.BoolKind:
		return &Shape{Kind: KindBool}
	case protoreflect.BytesKind:
		return &Shape{Kind: KindBytes}
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		return &Shape{Kind: KindFloat}
	case protoreflect.Int32Kind, protoreflect.Int64Kind, protoreflect.Sint32Kind, protoreflect.Sint64Kind,
		protoreflect.Uint32Kind, protoreflect.Uint64Kind, protoreflect.Fixed32Kind, protoreflect.Fixed64Kind,
		protoreflect.Sfixed32Kind, protoreflect.Sfixed64Kind:
		return &Shape{Kind: KindInt}
	case protoreflect.EnumKind:
		s := &Shape{Kind: KindEnum, Name: string(fd.Enum().FullName())}
		values := fd.Enum().Values()
		for i := 0; i < values.Len(); i++ {
			if values.Get(i).Number() == 0 {
				continue
			}
			s.Enum = append(s.Enum, string(values.Get(i).Name()))
		}
		return s
	case protoreflect.MessageKind, protoreflect.GroupKind:
		switch fd.Message().FullName() {
		case "google.protobuf.Timestamp":
			return &Shape{Kind: KindTime}
		case "google.protobuf.Struct", "google.protobuf.Value", "google.protobuf.Any", "google.protobuf.Duration":
			return &Shape{Kind: KindAny}
		}
		return c.message(fd.Message())
	}
	return &Shape{Kind: KindAny}
}
