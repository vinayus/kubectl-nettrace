package gateway

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

var (
	HTTPRouteGVR = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}
	GRPCRouteGVR = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "grpcroutes"}
)

type RouteInfo struct {
	Kind      string
	Name      string
	Namespace string
	Gateway   string
	Backends  []string
}

func IsAvailable(cs *kubernetes.Clientset) bool {
	_, resources, err := cs.Discovery().ServerGroupsAndResources()
	if err != nil {
		return false
	}
	for _, list := range resources {
		if list.GroupVersion == "gateway.networking.k8s.io/v1" {
			for _, r := range list.APIResources {
				if r.Name == "httproutes" {
					return true
				}
			}
		}
	}
	return false
}

func FetchRoutes(ctx context.Context, dyn dynamic.Interface, ns string, svcName string) ([]RouteInfo, error) {
	var routes []RouteInfo

	for _, gvr := range []schema.GroupVersionResource{HTTPRouteGVR, GRPCRouteGVR} {
		list, err := dyn.Resource(gvr).Namespace(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			continue
		}
		for _, item := range list.Items {
			if routeTargets(item, svcName) {
				routes = append(routes, parseRoute(item, gvr.Resource))
			}
		}
	}
	return routes, nil
}

func routeTargets(obj unstructured.Unstructured, svcName string) bool {
	rules, _, _ := unstructured.NestedSlice(obj.Object, "spec", "rules")
	for _, rule := range rules {
		ruleMap, ok := rule.(map[string]interface{})
		if !ok {
			continue
		}
		backends, _, _ := unstructured.NestedSlice(ruleMap, "backendRefs")
		for _, be := range backends {
			beMap, ok := be.(map[string]interface{})
			if !ok {
				continue
			}
			if name, _, _ := unstructured.NestedString(beMap, "name"); name == svcName {
				return true
			}
		}
	}
	return false
}

func parseRoute(obj unstructured.Unstructured, kind string) RouteInfo {
	info := RouteInfo{
		Kind:      kind,
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
	}

	// extract parent gateway name
	parents, _, _ := unstructured.NestedSlice(obj.Object, "spec", "parentRefs")
	for _, p := range parents {
		pMap, ok := p.(map[string]interface{})
		if !ok {
			continue
		}
		if name, _, _ := unstructured.NestedString(pMap, "name"); name != "" {
			info.Gateway = name
			break
		}
	}

	// extract backend service names
	rules, _, _ := unstructured.NestedSlice(obj.Object, "spec", "rules")
	for _, rule := range rules {
		ruleMap, ok := rule.(map[string]interface{})
		if !ok {
			continue
		}
		backends, _, _ := unstructured.NestedSlice(ruleMap, "backendRefs")
		for _, be := range backends {
			beMap, ok := be.(map[string]interface{})
			if !ok {
				continue
			}
			if name, _, _ := unstructured.NestedString(beMap, "name"); name != "" {
				info.Backends = append(info.Backends, name)
			}
		}
	}
	return info
}
