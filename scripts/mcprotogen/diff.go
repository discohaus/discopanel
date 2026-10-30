package main

import (
	"archive/zip"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Classes whose bytecode shapes the hub wire format
var hubClasses = map[string]bool{
	"ClientboundLoginFinishedPacket": true, "ServerboundLoginAcknowledgedPacket": true,
	"ClientboundRegistryDataPacket": true, "ClientboundUpdateEnabledFeaturesPacket": true,
	"ClientboundFinishConfigurationPacket": true, "ServerboundFinishConfigurationPacket": true,
	"ClientboundSelectKnownPacks": true, "ServerboundSelectKnownPacks": true, "ClientboundUpdateTagsPacket": true,
	"ClientboundCustomPayloadPacket": true, "ServerboundCustomPayloadPacket": true, "ClientboundPingPacket": true,
	"ServerboundPongPacket": true, "ClientboundDisconnectPacket": true, "ClientboundKeepAlivePacket": true,
	"ServerboundKeepAlivePacket": true, "ClientboundLoginPacket": true, "CommonPlayerSpawnInfo": true,
	"ClientboundGameEventPacket": true, "ClientboundSetChunkCacheCenterPacket": true,
	"ClientboundSetChunkCacheRadiusPacket": true, "ClientboundLevelChunkWithLightPacket": true,
	"ClientboundLevelChunkPacketData": true, "ClientboundLightUpdatePacketData": true,
	"ClientboundForgetLevelChunkPacket": true, "ClientboundSetDefaultSpawnPositionPacket": true,
	"ClientboundPlayerAbilitiesPacket": true, "ClientboundSetTimePacket": true,
	"ClientboundPlayerInfoUpdatePacket": true, "ClientboundPlayerInfoRemovePacket": true,
	"ClientboundPlayerPositionPacket": true, "ClientboundAddEntityPacket": true,
	"ClientboundEntityPositionSyncPacket": true, "ClientboundTeleportEntityPacket": true,
	"ClientboundRotateHeadPacket": true, "ClientboundRemoveEntitiesPacket": true,
	"ClientboundSystemChatPacket": true, "ClientboundBlockUpdatePacket": true,
	"ClientboundBlockEntityDataPacket": true, "ClientboundTransferPacket": true,
	"ServerboundMovePlayerPacket": true, "ServerboundChatPacket": true,
	"ServerboundAcceptTeleportationPacket": true, "ServerboundClientCommandPacket": true,
	"LevelChunkSection": true, "PalettedContainer": true, "Heightmap": true, "PositionMoveRotation": true,
	"PositionPath": true, "SignBlockEntity": true, "SignText": true, "RegistryDataLoader": true,
	"KnownPack": true, "GameType": true, "DimensionType": true, "Relative": true, "ByteBufCodecs": true,
}

var (
	poolRef    = regexp.MustCompile(`#\d+`)
	byteOffset = regexp.MustCompile(`(?m)^\s*\d+: `)
	branchDest = regexp.MustCompile(`\b(if\w*|goto|goto_w|jsr)\s+\d+`)
)

// Lists hub relevant classes whose bytecode differs between two jars
func runDiff(args []string) error {
	fs := flag.NewFlagSet("diff", flag.ContinueOnError)
	javaBin := fs.String("java", "java", "java binary, javap must sit beside it")
	cache := fs.String("cache", defaultCache(), "jar cache")
	full := fs.Bool("full", false, "print every differing line")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("name two versions")
	}
	m, err := fetchManifest()
	if err != nil {
		return err
	}
	var inner [2]string
	for i, id := range fs.Args() {
		entry, ok := m.find(id)
		if !ok {
			return fmt.Errorf("version %q is not in the manifest", id)
		}
		meta, err := fetchMeta(*cache, entry)
		if err != nil {
			return err
		}
		jar := filepath.Join(*cache, id, "server.jar")
		if err := downloadFile(meta.Downloads["server"], jar); err != nil {
			return err
		}
		if inner[i], err = innerJar(jar, filepath.Join(*cache, id, "inner")); err != nil {
			return err
		}
	}
	javap := filepath.Join(filepath.Dir(*javaBin), "javap")
	if *javaBin == "java" {
		javap = "javap"
	}
	a, err := classList(inner[0])
	if err != nil {
		return err
	}
	b, err := classList(inner[1])
	if err != nil {
		return err
	}
	changed := 0
	for _, cls := range sortedKeys(a) {
		if !b[cls] {
			fmt.Printf("removed  %s\n", cls)
			changed++
			continue
		}
		da, err := disassemble(javap, inner[0], cls)
		if err != nil {
			return err
		}
		db, err := disassemble(javap, inner[1], cls)
		if err != nil {
			return err
		}
		if da == db {
			continue
		}
		changed++
		fmt.Printf("changed  %s\n", cls)
		if *full {
			fmt.Println(lineDiff(da, db))
		}
	}
	for _, cls := range sortedKeys(b) {
		if !a[cls] {
			fmt.Printf("added    %s\n", cls)
			changed++
		}
	}
	fmt.Printf("%d hub relevant classes differ between %s and %s\n", changed, fs.Arg(0), fs.Arg(1))
	return nil
}

// Extracts the bundled server jar, older jars are their own
func innerJar(jar, dir string) (string, error) {
	zr, err := zip.OpenReader(jar)
	if err != nil {
		return "", err
	}
	defer zr.Close()
	for _, f := range zr.File {
		if !strings.HasPrefix(f.Name, "META-INF/versions/") || !strings.HasSuffix(f.Name, ".jar") {
			continue
		}
		out := filepath.Join(dir, filepath.Base(f.Name))
		if _, err := os.Stat(out); err == nil {
			return out, nil
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		defer rc.Close()
		w, err := os.Create(out)
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(w, rc); err != nil {
			w.Close()
			return "", err
		}
		return out, w.Close()
	}
	return jar, nil
}

// Fully qualified hub relevant classes inside one jar
func classList(jar string) (map[string]bool, error) {
	zr, err := zip.OpenReader(jar)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	out := map[string]bool{}
	for _, f := range zr.File {
		if !strings.HasSuffix(f.Name, ".class") || !strings.HasPrefix(f.Name, "net/minecraft/") {
			continue
		}
		simple := strings.TrimSuffix(filepath.Base(f.Name), ".class")
		if i := strings.IndexByte(simple, '$'); i >= 0 {
			simple = simple[:i]
		}
		if hubClasses[simple] {
			out[strings.ReplaceAll(strings.TrimSuffix(f.Name, ".class"), "/", ".")] = true
		}
	}
	return out, nil
}

// Disassembly with constant pool indices and offsets stripped
func disassemble(javap, jar, cls string) (string, error) {
	out, err := exec.Command(javap, "-c", "-p", "-cp", jar, cls).Output()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w", javap, cls, err)
	}
	s := poolRef.ReplaceAllString(string(out), "#")
	s = byteOffset.ReplaceAllString(s, "  ")
	s = branchDest.ReplaceAllString(s, "$1 N")
	return s, nil
}

// Lines present on one side only, marked by side
func lineDiff(a, b string) string {
	la, lb := strings.Split(a, "\n"), strings.Split(b, "\n")
	setA, setB := map[string]int{}, map[string]int{}
	for _, l := range la {
		setA[l]++
	}
	for _, l := range lb {
		setB[l]++
	}
	var out []string
	for _, l := range la {
		if setB[l] == 0 {
			out = append(out, "- "+l)
		}
	}
	for _, l := range lb {
		if setA[l] == 0 {
			out = append(out, "+ "+l)
		}
	}
	sort.Stable(sort.StringSlice(out))
	return strings.Join(out, "\n")
}
