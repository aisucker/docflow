package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"time"

	"github.com/aisucker/docflow/mcp/internal/docflow"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run() error {
	log.SetOutput(os.Stderr)
	log.SetFlags(0)
	path := flag.String("config-file", os.Getenv("DOCFLOW_CONFIG_FILE"), "JSON with API user credentials (never exposed as a tool)")
	readOnly := flag.Bool("read-only", false, "expose only read tools")
	check := flag.Bool("check", false, "verify the API account and exit without starting MCP")
	flag.Parse()
	cfg, err := docflow.LoadConfig(*path)
	if err != nil {
		return err
	}
	cfg.ReadOnly = cfg.ReadOnly || *readOnly
	client, err := docflow.NewClient(cfg)
	if err != nil {
		return err
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		client.Close(ctx)
	}()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if *check {
		data, err := client.Do(ctx, "GET", "/me", nil)
		if err != nil {
			return err
		}
		me, _ := data.(map[string]any)
		fmt.Fprintf(os.Stderr, "Docflow API OK; account: %s; role: %s; read-only: %t\n", me["full_name"], me["role"], cfg.ReadOnly)
		return nil
	}
	if err := docflow.NewServer(client).Run(ctx, &mcp.StdioTransport{}); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}
