package spex

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed schemas/*.schema.json
var schemaFS embed.FS

func runSchema(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		schemaUsage(stdout)
		return nil
	}
	switch args[0] {
	case "help", "--help", "-h":
		if len(args) > 1 {
			return fmt.Errorf("schema help does not accept positional arguments: %s", strings.Join(args[1:], ", "))
		}
		schemaUsage(stdout)
		return nil
	case "list":
		return runSchemaList(args[1:], stdout)
	case "show":
		if len(args) != 2 {
			return fmt.Errorf("schema show requires a schema name")
		}
		return runSchemaShow(args[1], stdout)
	default:
		return fmt.Errorf("unknown schema command %q", args[0])
	}
}

func schemaUsage(stdout io.Writer) {
	fmt.Fprintln(stdout, `usage: spex schema <command> [flags]

Schema commands:
  list  list embedded JSON Schema names
  show  print one embedded JSON Schema

Examples:
  spex schema list --format json
  spex schema show scenario-suite > scenario-suite.schema.json`)
}

type schemaListOutput struct {
	Schemas []string `json:"schemas"`
}

func runSchemaList(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("schema list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	format := fs.String("format", "text", "output format: text or json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("schema list does not accept positional arguments")
	}
	names, err := schemaNames()
	if err != nil {
		return err
	}
	switch *format {
	case "text":
	case "json":
		content, err := json.MarshalIndent(schemaListOutput{Schemas: names}, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, string(content))
		return nil
	default:
		return fmt.Errorf("schema list --format must be text or json")
	}
	for _, name := range names {
		fmt.Fprintln(stdout, name)
	}
	return nil
}

func runSchemaShow(name string, stdout io.Writer) error {
	name = strings.TrimSuffix(name, ".schema.json")
	name = strings.TrimSuffix(name, ".json")
	content, err := schemaFS.ReadFile(filepath.ToSlash(filepath.Join("schemas", name+".schema.json")))
	if err != nil {
		known, knownErr := schemaNames()
		if knownErr != nil {
			return knownErr
		}
		return fmt.Errorf("unknown schema %q. Known schemas: %s", name, strings.Join(known, ", "))
	}
	fmt.Fprint(stdout, string(content))
	if len(content) == 0 || content[len(content)-1] != '\n' {
		fmt.Fprintln(stdout)
	}
	return nil
}

func schemaNames() ([]string, error) {
	entries, err := schemaFS.ReadDir("schemas")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".schema.json") {
			continue
		}
		names = append(names, strings.TrimSuffix(entry.Name(), ".schema.json"))
	}
	sort.Strings(names)
	return names, nil
}
