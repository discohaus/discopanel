package main

import (
	"flag"
	"fmt"
	"os"

	"ariga.io/atlas-provider-gorm/gormschema"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"gorm.io/gorm"
)

// Prints the sqlite DDL atlas diffs migrations against
func main() {
	out := flag.String("out", "", "schema file to write, stdout when empty")
	flag.Parse()
	// Foreign keys stay off, store sweeps child rows itself
	loader := gormschema.New("sqlite", gormschema.WithConfig(&gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	}))
	ddl, err := loader.Load(v1.AllModels()...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load gorm schema: %v\n", err)
		os.Exit(1)
	}
	if *out == "" {
		fmt.Print(ddl)
		return
	}
	if err := os.WriteFile(*out, []byte(ddl), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write %s: %v\n", *out, err)
		os.Exit(1)
	}
}
