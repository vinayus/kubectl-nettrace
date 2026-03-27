package netpol

import (
	"context"
	"net"

	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"

	"github.com/vinayus/kubectl-nettrace/internal/resolve"
)

type Direction int

const (
	Egress  Direction = iota
	Ingress
)

type Verdict int

const (
	VerdictUnrestricted Verdict = iota // no policy selects the pod
	VerdictAllow                       // policy selects and permits
	VerdictDeny                        // policy selects but no matching rule
)

type Result struct {
	Verdict          Verdict
	SelectingPolices []string // policies that select the subject pod
	AllowedBy        string   // policy name that allowed traffic
}

func Evaluate(
	ctx context.Context,
	cs *kubernetes.Clientset,
	dir Direction,
	subject *resolve.ResolvedWorkload,
	peer *resolve.ResolvedWorkload,
	port *int32,
) (Result, error) {
	if len(subject.Pods) == 0 {
		return Result{Verdict: VerdictUnrestricted}, nil
	}

	policies, err := cs.NetworkingV1().NetworkPolicies(subject.Ref.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return Result{}, err
	}

	// fetch peer namespace labels for namespaceSelector matching
	peerNs, err := cs.CoreV1().Namespaces().Get(ctx, peer.Ref.Namespace, metav1.GetOptions{})
	if err != nil {
		return Result{}, err
	}

	representative := subject.Pods[0]
	var selecting []netv1.NetworkPolicy

	for _, pol := range policies.Items {
		if !hasDirection(pol, dir) {
			continue
		}
		sel, err := metav1.LabelSelectorAsSelector(&pol.Spec.PodSelector)
		if err != nil {
			continue
		}
		if sel.Matches(labels.Set(representative.Labels)) {
			selecting = append(selecting, pol)
		}
	}

	if len(selecting) == 0 {
		return Result{Verdict: VerdictUnrestricted}, nil
	}

	names := make([]string, len(selecting))
	for i, p := range selecting {
		names[i] = p.Name
	}

	for _, pol := range selecting {
		if matchesRules(pol, dir, peer.Pods, peer.Ref.Namespace, peerNs.Labels, subject.Ref.Namespace, port) {
			return Result{
				Verdict:          VerdictAllow,
				SelectingPolices: names,
				AllowedBy:        pol.Name,
			}, nil
		}
	}

	return Result{Verdict: VerdictDeny, SelectingPolices: names}, nil
}

func hasDirection(pol netv1.NetworkPolicy, dir Direction) bool {
	if len(pol.Spec.PolicyTypes) == 0 {
		// default: ingress only
		return dir == Ingress
	}
	for _, pt := range pol.Spec.PolicyTypes {
		if dir == Egress && pt == netv1.PolicyTypeEgress {
			return true
		}
		if dir == Ingress && pt == netv1.PolicyTypeIngress {
			return true
		}
	}
	return false
}

func matchesRules(
	pol netv1.NetworkPolicy,
	dir Direction,
	peerPods []corev1.Pod,
	peerNs string,
	peerNsLabels map[string]string,
	subjectNs string,
	port *int32,
) bool {
	if dir == Egress {
		for _, rule := range pol.Spec.Egress {
			if matchesPort(rule.Ports, port) && matchesToPeers(rule.To, peerPods, peerNs, peerNsLabels, subjectNs) {
				return true
			}
		}
	} else {
		for _, rule := range pol.Spec.Ingress {
			if matchesPort(rule.Ports, port) && matchesFromPeers(rule.From, peerPods, peerNs, peerNsLabels, subjectNs) {
				return true
			}
		}
	}
	return false
}

func matchesPort(rulePorts []netv1.NetworkPolicyPort, port *int32) bool {
	if len(rulePorts) == 0 || port == nil {
		return true
	}
	for _, rp := range rulePorts {
		if rp.Port == nil {
			return true
		}
		if rp.Port.IntValue() == int(*port) {
			return true
		}
	}
	return false
}

func matchesToPeers(peers []netv1.NetworkPolicyPeer, pods []corev1.Pod, ns string, nsLabels map[string]string, subjectNs string) bool {
	if len(peers) == 0 {
		return true
	}
	for _, peer := range peers {
		if matchesPeer(peer, pods, ns, nsLabels, subjectNs) {
			return true
		}
	}
	return false
}

func matchesFromPeers(peers []netv1.NetworkPolicyPeer, pods []corev1.Pod, ns string, nsLabels map[string]string, subjectNs string) bool {
	return matchesToPeers(peers, pods, ns, nsLabels, subjectNs)
}

func matchesPeer(peer netv1.NetworkPolicyPeer, pods []corev1.Pod, ns string, nsLabels map[string]string, subjectNs string) bool {
	if peer.IPBlock != nil {
		for _, pod := range pods {
			if matchesIPBlock(peer.IPBlock, pod.Status.PodIP) {
				return true
			}
		}
		return false
	}

	hasPodSel := peer.PodSelector != nil
	hasNsSel := peer.NamespaceSelector != nil

	// only podSelector — matches pods in same namespace as the policy
	if hasPodSel && !hasNsSel {
		if ns != subjectNs {
			return false
		}
		sel, err := metav1.LabelSelectorAsSelector(peer.PodSelector)
		if err != nil {
			return false
		}
		for _, pod := range pods {
			if sel.Matches(labels.Set(pod.Labels)) {
				return true
			}
		}
		return false
	}

	// only namespaceSelector — matches any pod in a matching namespace
	if hasNsSel && !hasPodSel {
		sel, err := metav1.LabelSelectorAsSelector(peer.NamespaceSelector)
		if err != nil {
			return false
		}
		return sel.Matches(labels.Set(nsLabels))
	}

	// both — namespace must match AND pod must match
	if hasPodSel && hasNsSel {
		nsSel, err := metav1.LabelSelectorAsSelector(peer.NamespaceSelector)
		if err != nil {
			return false
		}
		if !nsSel.Matches(labels.Set(nsLabels)) {
			return false
		}
		podSel, err := metav1.LabelSelectorAsSelector(peer.PodSelector)
		if err != nil {
			return false
		}
		for _, pod := range pods {
			if podSel.Matches(labels.Set(pod.Labels)) {
				return true
			}
		}
		return false
	}

	// neither selector — matches everything
	return true
}

func matchesIPBlock(block *netv1.IPBlock, ip string) bool {
	if ip == "" {
		return false
	}
	_, cidr, err := net.ParseCIDR(block.CIDR)
	if err != nil {
		return false
	}
	parsed := net.ParseIP(ip)
	if parsed == nil || !cidr.Contains(parsed) {
		return false
	}
	for _, except := range block.Except {
		_, exceptCIDR, err := net.ParseCIDR(except)
		if err != nil {
			continue
		}
		if exceptCIDR.Contains(parsed) {
			return false
		}
	}
	return true
}
