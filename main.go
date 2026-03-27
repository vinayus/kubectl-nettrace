package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/pflag"
	"github.com/vinayus/kubectl-nettrace/internal/output"
	"github.com/vinayus/kubectl-nettrace/internal/resolve"
	"github.com/vinayus/kubectl-nettrace/internal/trace"
	"github.com/vinayus/kubectl-nettrace/pkg/k8s"
)

func main() {
	flags := pflag.NewFlagSet("kubectl-nettrace", pflag.ExitOnError)
	ns := flags.StringP("namespace", "n", "default", "default namespace for both source and target")
	srcNs := flags.String("src-ns", "", "source namespace (overrides -n for source)")
	dstNs := flags.String("dst-ns", "", "destination namespace (overrides -n for destination)")
	port := flags.Int("port", 0, "port to evaluate (0 = any port)")

	flags.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: kubectl nettrace <source> <target> [flags]\n\n")
		fmt.Fprintf(os.Stderr, "Examples:\n")
		fmt.Fprintf(os.Stderr, "  kubectl nettrace pod/api pod/postgres -n production\n")
		fmt.Fprintf(os.Stderr, "  kubectl nettrace deploy/api svc/postgres -n production\n")
		fmt.Fprintf(os.Stderr, "  kubectl nettrace deploy/api sts/postgres --src-ns frontend --dst-ns data\n\n")
		fmt.Fprintf(os.Stderr, "Supported types: pod, deploy, sts, svc\n\n")
		flags.PrintDefaults()
	}
	flags.Parse(os.Args[1:])

	args := flags.Args()
	if len(args) != 2 {
		flags.Usage()
		os.Exit(1)
	}

	defaultNs := *ns

	srcRef, err := resolve.Parse(args[0], defaultNs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	dstRef, err := resolve.Parse(args[1], defaultNs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if *srcNs != "" {
		srcRef.Namespace = *srcNs
	}
	if *dstNs != "" {
		dstRef.Namespace = *dstNs
	}

	clients, err := k8s.Build()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting to cluster: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	src, err := resolve.Resolve(ctx, clients.Typed, srcRef)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving source: %v\n", err)
		os.Exit(1)
	}

	dst, err := resolve.Resolve(ctx, clients.Typed, dstRef)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving target: %v\n", err)
		os.Exit(1)
	}

	var portPtr *int32
	if *port != 0 {
		p := int32(*port)
		portPtr = &p
	}

	result, err := trace.Run(ctx, clients, src, dst, portPtr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error tracing path: %v\n", err)
		os.Exit(1)
	}

	output.Print(os.Stdout, result)

	if result.HasFailure {
		os.Exit(1)
	}
}
