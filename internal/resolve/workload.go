package resolve

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
)

type ResourceKind string

const (
	KindPod         ResourceKind = "pod"
	KindDeployment  ResourceKind = "deploy"
	KindStatefulSet ResourceKind = "sts"
	KindService     ResourceKind = "svc"
)

type WorkloadRef struct {
	Kind      ResourceKind
	Name      string
	Namespace string
}

type ResolvedWorkload struct {
	Ref      WorkloadRef
	Pods     []corev1.Pod
	Selector labels.Selector
}

func Parse(arg, defaultNamespace string) (WorkloadRef, error) {
	parts := strings.SplitN(arg, "/", 2)
	if len(parts) != 2 {
		return WorkloadRef{}, fmt.Errorf("invalid format %q — expected type/name (e.g. pod/api, deploy/api)", arg)
	}
	kind := strings.ToLower(parts[0])
	switch kind {
	case "deployment":
		kind = "deploy"
	case "statefulset":
		kind = "sts"
	case "service":
		kind = "svc"
	case "pod", "deploy", "sts", "svc":
	default:
		return WorkloadRef{}, fmt.Errorf("unsupported type %q — supported: pod, deploy, sts, svc", parts[0])
	}
	return WorkloadRef{Kind: ResourceKind(kind), Name: parts[1], Namespace: defaultNamespace}, nil
}

func Resolve(ctx context.Context, cs *kubernetes.Clientset, ref WorkloadRef) (*ResolvedWorkload, error) {
	switch ref.Kind {
	case KindPod:
		return resolvePod(ctx, cs, ref)
	case KindDeployment:
		return resolveDeployment(ctx, cs, ref)
	case KindStatefulSet:
		return resolveStatefulSet(ctx, cs, ref)
	case KindService:
		return resolveService(ctx, cs, ref)
	}
	return nil, fmt.Errorf("unknown kind %q", ref.Kind)
}

func resolvePod(ctx context.Context, cs *kubernetes.Clientset, ref WorkloadRef) (*ResolvedWorkload, error) {
	pod, err := cs.CoreV1().Pods(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("pod %s/%s not found: %w", ref.Namespace, ref.Name, err)
	}
	sel := labels.Set(pod.Labels).AsSelector()
	return &ResolvedWorkload{Ref: ref, Pods: []corev1.Pod{*pod}, Selector: sel}, nil
}

func resolveDeployment(ctx context.Context, cs *kubernetes.Clientset, ref WorkloadRef) (*ResolvedWorkload, error) {
	deploy, err := cs.AppsV1().Deployments(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("deployment %s/%s not found: %w", ref.Namespace, ref.Name, err)
	}
	sel, err := metav1.LabelSelectorAsSelector(deploy.Spec.Selector)
	if err != nil {
		return nil, err
	}
	pods, err := listPodsBySelector(ctx, cs, ref.Namespace, sel)
	if err != nil {
		return nil, err
	}
	return &ResolvedWorkload{Ref: ref, Pods: pods, Selector: sel}, nil
}

func resolveStatefulSet(ctx context.Context, cs *kubernetes.Clientset, ref WorkloadRef) (*ResolvedWorkload, error) {
	sts, err := cs.AppsV1().StatefulSets(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("statefulset %s/%s not found: %w", ref.Namespace, ref.Name, err)
	}
	sel, err := metav1.LabelSelectorAsSelector(sts.Spec.Selector)
	if err != nil {
		return nil, err
	}
	pods, err := listPodsBySelector(ctx, cs, ref.Namespace, sel)
	if err != nil {
		return nil, err
	}
	return &ResolvedWorkload{Ref: ref, Pods: pods, Selector: sel}, nil
}

func resolveService(ctx context.Context, cs *kubernetes.Clientset, ref WorkloadRef) (*ResolvedWorkload, error) {
	svc, err := cs.CoreV1().Services(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("service %s/%s not found: %w", ref.Namespace, ref.Name, err)
	}
	if svc.Spec.Selector == nil {
		return &ResolvedWorkload{Ref: ref, Pods: nil, Selector: labels.Nothing()}, nil
	}
	sel := labels.Set(svc.Spec.Selector).AsSelector()
	pods, err := listPodsBySelector(ctx, cs, ref.Namespace, sel)
	if err != nil {
		return nil, err
	}
	return &ResolvedWorkload{Ref: ref, Pods: pods, Selector: sel}, nil
}

func listPodsBySelector(ctx context.Context, cs *kubernetes.Clientset, ns string, sel labels.Selector) ([]corev1.Pod, error) {
	list, err := cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: sel.String(),
	})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

func IsPodReady(pod corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}
