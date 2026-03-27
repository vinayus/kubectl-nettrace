package trace

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/vinayus/kubectl-nettrace/internal/gateway"
	"github.com/vinayus/kubectl-nettrace/internal/netpol"
	"github.com/vinayus/kubectl-nettrace/internal/resolve"
	"github.com/vinayus/kubectl-nettrace/pkg/k8s"
)

type HopStatus int

const (
	StatusOK   HopStatus = iota // ✓
	StatusWarn                   // ~
	StatusFail                   // ✗
)

type SubRow struct {
	Name    string
	Status  HopStatus
	Message string
}

type Hop struct {
	Number  int
	Kind    string
	Name    string
	Status  HopStatus
	Message string
	SubRows []SubRow
}

type Result struct {
	Header     string
	Hops       []Hop
	HasFailure bool
	HasWarning bool
	Summary    string
}

func Run(ctx context.Context, clients *k8s.Clients, src, dst *resolve.ResolvedWorkload, port *int32) (*Result, error) {
	var hops []Hop
	hasFailure := false
	hasWarning := false

	// Hop: source health
	hops = append(hops, sourceHealthHop(src))

	// Hop: egress NetworkPolicy
	egressHop, err := egressPolicyHop(ctx, clients, src, dst, port)
	if err != nil {
		return nil, fmt.Errorf("egress policy check: %w", err)
	}
	hops = append(hops, egressHop)

	// Hop: DNS (cross-namespace only)
	if src.Ref.Namespace != dst.Ref.Namespace {
		hops = append(hops, dnsHop(src, dst))
	}

	// Hop: Service (if target is svc, or find a service in front of target)
	svc, svcHops, err := serviceHops(ctx, clients, dst)
	if err != nil {
		return nil, fmt.Errorf("service check: %w", err)
	}
	hops = append(hops, svcHops...)

	// Hop: EndpointSlice (if we found a service)
	if svc != nil {
		esHop, err := endpointSliceHop(ctx, clients, svc)
		if err != nil {
			return nil, fmt.Errorf("endpoint check: %w", err)
		}
		hops = append(hops, esHop)
	}

	// Hop: Gateway API (if CRDs present and target is a service)
	if svc != nil && gateway.IsAvailable(clients.Typed) {
		gwHops, err := gatewayHops(ctx, clients, dst, svc)
		if err == nil {
			hops = append(hops, gwHops...)
		}
	}

	// Hop: ingress NetworkPolicy
	ingressHop, err := ingressPolicyHop(ctx, clients, src, dst, port)
	if err != nil {
		return nil, fmt.Errorf("ingress policy check: %w", err)
	}
	hops = append(hops, ingressHop)

	// Hop: target health
	hops = append(hops, targetHealthHop(dst))

	// number hops sequentially
	for i := range hops {
		hops[i].Number = i + 1
	}

	// compute outcome
	for _, h := range hops {
		if h.Status == StatusFail {
			hasFailure = true
		}
		if h.Status == StatusWarn {
			hasWarning = true
		}
	}

	summary := "✓ Path clear"
	if hasFailure {
		for _, h := range hops {
			if h.Status == StatusFail {
				summary = fmt.Sprintf("✗ Blocked at hop %d (%s: %s)", h.Number, h.Kind, h.Name)
				break
			}
		}
	} else if hasWarning {
		summary = "✓ Path clear (with warnings)"
	}

	header := fmt.Sprintf("Tracing: %s/%s [%s] → %s/%s [%s]",
		src.Ref.Kind, src.Ref.Name, src.Ref.Namespace,
		dst.Ref.Kind, dst.Ref.Name, dst.Ref.Namespace)

	return &Result{
		Header:     header,
		Hops:       hops,
		HasFailure: hasFailure,
		HasWarning: hasWarning,
		Summary:    summary,
	}, nil
}

func sourceHealthHop(w *resolve.ResolvedWorkload) Hop {
	total := len(w.Pods)
	ready := 0
	for _, p := range w.Pods {
		if resolve.IsPodReady(p) {
			ready++
		}
	}
	status := StatusOK
	msg := fmt.Sprintf("%d/%d ready", ready, total)
	if total == 0 {
		status = StatusWarn
		msg = "no pods found"
	} else if ready < total {
		status = StatusWarn
	}
	return Hop{
		Kind:    kindLabel(w.Ref.Kind),
		Name:    w.Ref.Name,
		Status:  status,
		Message: msg,
	}
}

func targetHealthHop(w *resolve.ResolvedWorkload) Hop {
	return sourceHealthHop(w)
}

func egressPolicyHop(ctx context.Context, clients *k8s.Clients, src, dst *resolve.ResolvedWorkload, port *int32) (Hop, error) {
	res, err := netpol.Evaluate(ctx, clients.Typed, netpol.Egress, src, dst, port)
	if err != nil {
		return Hop{}, err
	}
	return policyHop("Egress Policy", res), nil
}

func ingressPolicyHop(ctx context.Context, clients *k8s.Clients, src, dst *resolve.ResolvedWorkload, port *int32) (Hop, error) {
	res, err := netpol.Evaluate(ctx, clients.Typed, netpol.Ingress, dst, src, port)
	if err != nil {
		return Hop{}, err
	}
	return policyHop("Ingress Policy", res), nil
}

func policyHop(kind string, res netpol.Result) Hop {
	switch res.Verdict {
	case netpol.VerdictUnrestricted:
		return Hop{Kind: kind, Name: "(none)", Status: StatusWarn, Message: "no policies — all traffic allowed"}
	case netpol.VerdictAllow:
		return Hop{Kind: kind, Name: res.AllowedBy, Status: StatusOK, Message: "allowed"}
	default:
		return Hop{
			Kind:    kind,
			Name:    strings.Join(res.SelectingPolices, ", "),
			Status:  StatusFail,
			Message: "no matching rule — traffic blocked",
		}
	}
}

func dnsHop(src, dst *resolve.ResolvedWorkload) Hop {
	fqdn := fmt.Sprintf("%s.%s.svc.cluster.local", dst.Ref.Name, dst.Ref.Namespace)
	return Hop{
		Kind:    "DNS",
		Name:    fqdn,
		Status:  StatusWarn,
		Message: fmt.Sprintf("cross-namespace: use FQDN, not %q", dst.Ref.Name),
	}
}

func serviceHops(ctx context.Context, clients *k8s.Clients, dst *resolve.ResolvedWorkload) (*corev1.Service, []Hop, error) {
	var svcName string
	if dst.Ref.Kind == resolve.KindService {
		svcName = dst.Ref.Name
	} else {
		// try to find a service that selects the target pods
		svcs, err := clients.Typed.CoreV1().Services(dst.Ref.Namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, nil, err
		}
		if len(dst.Pods) > 0 {
			podLabels := dst.Pods[0].Labels
			for _, s := range svcs.Items {
				if len(s.Spec.Selector) == 0 {
					continue
				}
				if labelsMatch(s.Spec.Selector, podLabels) {
					svcName = s.Name
					break
				}
			}
		}
	}

	if svcName == "" {
		return nil, nil, nil
	}

	svc, err := clients.Typed.CoreV1().Services(dst.Ref.Namespace).Get(ctx, svcName, metav1.GetOptions{})
	if err != nil {
		return nil, []Hop{{
			Kind:    "Service",
			Name:    svcName,
			Status:  StatusFail,
			Message: "not found",
		}}, nil
	}

	svcHop := Hop{
		Kind:    "Service",
		Name:    fmt.Sprintf("%s (%s)", svc.Name, svc.Spec.Type),
		Status:  StatusOK,
		Message: fmt.Sprintf("ClusterIP: %s", svc.Spec.ClusterIP),
	}

	var hops []Hop
	hops = append(hops, svcHop)

	if svc.Spec.TrafficDistribution != nil {
		hops = append(hops, Hop{
			Kind:    "TrafficDist",
			Name:    *svc.Spec.TrafficDistribution,
			Status:  StatusWarn,
			Message: "zone/node-aware routing active",
		})
	}

	return svc, hops, nil
}

func endpointSliceHop(ctx context.Context, clients *k8s.Clients, svc *corev1.Service) (Hop, error) {
	slices, err := clients.Typed.DiscoveryV1().EndpointSlices(svc.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "kubernetes.io/service-name=" + svc.Name,
	})
	if err != nil {
		return Hop{Kind: "EndpointSlice", Name: svc.Name, Status: StatusFail, Message: err.Error()}, nil
	}

	hop := Hop{Kind: "EndpointSlice", Name: svc.Name, Status: StatusOK}

	totalReady := 0
	totalNotReady := 0

	for _, slice := range slices.Items {
		for _, ep := range slice.Endpoints {
			ready := ep.Conditions.Ready != nil && *ep.Conditions.Ready
			addr := ""
			if len(ep.Addresses) > 0 {
				addr = ep.Addresses[0]
			}
			name := addr
			if ep.TargetRef != nil {
				name = fmt.Sprintf("%s (%s)", ep.TargetRef.Name, addr)
			}
			if ready {
				totalReady++
				hop.SubRows = append(hop.SubRows, SubRow{Name: name, Status: StatusOK, Message: "ready"})
			} else {
				totalNotReady++
				hop.SubRows = append(hop.SubRows, SubRow{Name: name, Status: StatusFail, Message: "not ready"})
			}
		}
	}

	if totalNotReady > 0 {
		hop.Status = StatusWarn
		hop.Message = fmt.Sprintf("%d ready, %d not ready", totalReady, totalNotReady)
	} else if totalReady == 0 {
		hop.Status = StatusFail
		hop.Message = "no endpoints"
	} else {
		hop.Message = fmt.Sprintf("%d ready", totalReady)
	}

	return hop, nil
}

func gatewayHops(ctx context.Context, clients *k8s.Clients, dst *resolve.ResolvedWorkload, svc *corev1.Service) ([]Hop, error) {
	routes, err := gateway.FetchRoutes(ctx, clients.Dynamic, dst.Ref.Namespace, svc.Name)
	if err != nil || len(routes) == 0 {
		return nil, err
	}

	var hops []Hop
	for _, r := range routes {
		name := r.Name
		if r.Gateway != "" {
			name = fmt.Sprintf("%s (via Gateway/%s)", r.Name, r.Gateway)
		}
		hops = append(hops, Hop{
			Kind:    strings.ToUpper(r.Kind[:1]) + r.Kind[1:],
			Name:    name,
			Status:  StatusOK,
			Message: fmt.Sprintf("backends: %s", strings.Join(r.Backends, ", ")),
		})
	}
	return hops, nil
}

func kindLabel(k resolve.ResourceKind) string {
	switch k {
	case resolve.KindDeployment:
		return "Deployment"
	case resolve.KindStatefulSet:
		return "StatefulSet"
	case resolve.KindPod:
		return "Pod"
	case resolve.KindService:
		return "Service"
	}
	return string(k)
}

func labelsMatch(selector, podLabels map[string]string) bool {
	for k, v := range selector {
		if podLabels[k] != v {
			return false
		}
	}
	return true
}
