/*
@Time : 2020/11/23 14:50
@Author : Tux
@Description :
*/

package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"
	"time"

	"k8s.io/api/admission/v1beta1"
	admissionregistrationv1beta1 "k8s.io/api/admissionregistration/v1beta1"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/klog/v2"
)

var (
	// 定义基础工具
	runtimeScheme = runtime.NewScheme()
	codecs        = serializer.NewCodecFactory(runtimeScheme)
	deserializer  = codecs.UniversalDeserializer()

	// (https://github.com/kubernetes/kubernetes/issues/57982)
	_ = runtime.ObjectDefaulter(runtimeScheme)
)

var (
	ignoredNamespaces = []string{
		metav1.NamespaceSystem,
		metav1.NamespacePublic,
	}
	requiredLabels = []string{
		nameLabel,
		instanceLabel,
		versionLabel,
		componentLabel,
		partOfLabel,
		managedByLabel,
	}
	addLabels = map[string]string{
		nameLabel:      NA,
		instanceLabel:  NA,
		versionLabel:   NA,
		componentLabel: NA,
		partOfLabel:    NA,
		managedByLabel: NA,
	}
	addAnnotations = map[string]string{
		admissionWebhookAnnotationMutateKey:     NA,
		admissionWebhookAnnotationMutateTimeKey: NA,
	}
)

const (
	// https://github.com/json-patch/json-patch-tests/issues/42
	// "~"(tilde) is encoded as "~0" and "/"(forward slash) is encoded as "~1".
	admissionWebhookAnnotationValidateKey   = "admission-webhook-example.zouhl.com/validate"
	admissionWebhookAnnotationMutateKey     = "admission-webhook-example.zouhl.com/mutate"
	admissionWebhookAnnotationMutateTimeKey = "admission-webhook-example.zouhl.com/mutate-time"
	admissionWebhookAnnotationStatusKey     = "admission-webhook-example.zouhl.com/status"

	nameLabel      = "app.kubernetes.io/name"
	instanceLabel  = "app.kubernetes.io/instance"
	versionLabel   = "app.kubernetes.io/version"
	componentLabel = "app.kubernetes.io/component"
	partOfLabel    = "app.kubernetes.io/part-of"
	managedByLabel = "app.kubernetes.io/managed-of"

	NA = "not_available"
)

type WebhookServer struct {
	server *http.Server
}

type WHSvrParameters struct {
	port           int    // webhook server port
	certFile       string // path to the x509 certificate for https
	keyFile        string // path to the x509 private key matching `CertFile`
	sidecarCfgFile string // path to sidecar injector configuration file
}

type patchOperation struct {
	Op    string      `json:"op"`
	Path  string      `json:"path"`
	Value interface{} `json:"value,omitempty"`
}

func init() {
	_ = corev1.AddToScheme(runtimeScheme)
	_ = admissionregistrationv1beta1.AddToScheme(runtimeScheme)

	// defaulting with webhooks:
	// https://github.com/kubernetes/kubernetes/issues/57982
	_ = v1.AddToScheme(runtimeScheme)
}

// admissionRequired 判断是否填过该资源
func admissionRequired(ignoredList []string, admissionAnnotationKey string, metadata *metav1.ObjectMeta) bool {
	// 跳过 ignoredList 里面的命名空间
	for _, namespace := range ignoredList {
		if metadata.Namespace == namespace {
			klog.Infof("Skip validation for %v for it's special namespace:%v", metadata.Name, metadata.Namespace)
			return false
		}
	}
	annotations := metadata.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	var required bool
	// 跳过注解 admissionAnnotationKey 为 false 的资源
	switch strings.ToLower(annotations[admissionAnnotationKey]) {
	default:
		required = true
	case "n", "no", "false", "off":
		required = false
	}
	return required
}

// MutationRequired 验证是否需要 mutate
func MutationRequired(ignoredList []string, metadata *metav1.ObjectMeta) bool {
	// 验证是否需要跳过
	required := admissionRequired(ignoredList, admissionWebhookAnnotationMutateKey, metadata)
	// 获取注解
	annotations := metadata.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	// 获取直接中的 admissionWebhookAnnotationStatusKey 的值
	status := annotations[admissionWebhookAnnotationStatusKey]

	// 如果 admissionWebhookAnnotationStatusKey = mutated , 则跳过
	if strings.ToLower(status) == "mutated" {
		required = false
	}
	klog.Infof("Mutation policy for %v/%v: required:%v", metadata.Namespace, metadata.Name, required)
	return required
}

// validationRequired  验证是否需要 validate
func validationRequired(ignoredList []string, metadata *metav1.ObjectMeta) bool {
	required := admissionRequired(ignoredList, admissionWebhookAnnotationValidateKey, metadata)
	klog.Infof("Validation policy for %v/%v: required:%v", metadata.Namespace, metadata.Name, required)
	return required
}

// updateAnnotations 更新注解,返回 patchOperation 切片
func updateAnnotations(target map[string]string, added map[string]string) (patch []patchOperation) {
	for key, value := range added {
		// https://tools.ietf.org/html/rfc6902#page-5
		patch = append(patch, patchOperation{
			Op:    "add",
			Path:  "/metadata/annotations/" + escapeJSONPointerValue(key),
			Value: value,
		})
		// if target == nil || target[key] == "" { // 如果 target 里面没有这个则patch
		// 	patch = append(patch, patchOperation{
		// 		Op:    "add",
		// 		Path:  "/metadata/annotations/" + escapeJSONPointerValue(key),
		// 		Value: value,
		// 	})
		// } else { // 存在的注解则替换
		// 	patch = append(patch, patchOperation{
		// 		Op:    "replace",
		// 		Path:  "/metadata/annotations/" + escapeJSONPointerValue(key),
		// 		Value: value,
		// 	})
		// }
	}
	return patch
}

// updateLabels 更新 label, 返回 patchOperation 切片
func updateLabels(target map[string]string, added map[string]string) (patch []patchOperation) {
	for key, value := range added {
		// https://tools.ietf.org/html/rfc6902#page-5
		patch = append(patch, patchOperation{
			Op:    "add",
			Path:  "/metadata/labels/" + escapeJSONPointerValue(key),
			Value: value,
		})
		// if target == nil || target[key] == "" {
		// 	patch = append(patch, patchOperation{
		// 		Op:    "add",
		// 		Path:  "/metadata/labels/" + escapeJSONPointerValue(key),
		// 		Value: value,
		// 	})
		// } else {
		// 	patch = append(patch, patchOperation{
		// 		Op:    "replace",
		// 		Path:  "/metadata/labels/" + escapeJSONPointerValue(key),
		// 		Value: value,
		// 	})
		// }
	}
	return patch
}

// createPatch 创建所有的patch, 并将 patch 汇聚到一起, 返回一个序列化的JSONPatch
func createPatch(availableAnnotation map[string]string, annotations map[string]string, availableLabels map[string]string, labels map[string]string) ([]byte, error) {
	var patch []patchOperation
	patch = append(patch, updateAnnotations(availableAnnotation, annotations)...)
	patch = append(patch, updateLabels(availableLabels, labels)...)

	return json.Marshal(patch)
}

// validate deployments and services
func (whsvr *WebhookServer) validate(ar *v1beta1.AdmissionReview) *v1beta1.AdmissionResponse {
	req := ar.Request
	var (
		availableLabels                 map[string]string
		objectMeta                      *metav1.ObjectMeta
		resourceNamespace, resourceName string
	)

	klog.Infof("AdmissionReview for Kind=%v, Namespace=%v Name=%v (%v) UID=%v patchOperation=%v UserInfo=%v",
		req.Kind, req.Namespace, req.Name, resourceName, req.UID, req.Operation, req.UserInfo)

	switch req.Kind.Kind {
	case "Deployment":
		var deployment appsv1.Deployment
		if err := json.Unmarshal(req.Object.Raw, &deployment); err != nil {
			klog.Errorf("could not unmarshal raw object: %v", err)
			return &v1beta1.AdmissionResponse{
				Result: &metav1.Status{
					Message: err.Error(),
				},
			}
		}
		resourceName, resourceNamespace, objectMeta = deployment.Name, deployment.Namespace, &deployment.ObjectMeta
		availableLabels = deployment.Labels
	case "Service":
		var service corev1.Service
		if err := json.Unmarshal(req.Object.Raw, &service); err != nil {
			klog.Errorf("could not unmarshal raw object: %v", err)
			return &v1beta1.AdmissionResponse{
				Result: &metav1.Status{
					Message: err.Error(),
				},
			}
		}
		resourceName, resourceNamespace, objectMeta = service.Name, service.Namespace, &service.ObjectMeta
		availableLabels = service.Labels
	}

	if !validationRequired(ignoredNamespaces, objectMeta) {
		klog.Infof("Skipping validation for %s/%s due to policy check", resourceNamespace, resourceName)
		return &v1beta1.AdmissionResponse{
			Allowed: true,
		}
	}

	allowed := true
	var result *metav1.Status
	klog.Info("available labels:", availableLabels)
	klog.Info("required labels", requiredLabels)
	for _, rl := range requiredLabels {
		if _, ok := availableLabels[rl]; !ok {
			allowed = false
			result = &metav1.Status{
				Reason: "required labels are not set",
			}
			break
		}
	}
	return &v1beta1.AdmissionResponse{
		Allowed: allowed,
		Result:  result,
	}
}

// mutate  main mutation process
func (whsvr *WebhookServer) mutate(ar *v1beta1.AdmissionReview) *v1beta1.AdmissionResponse {
	req := ar.Request
	var (
		availableAnnotations, availableLabels map[string]string
		objectMeta                            *metav1.ObjectMeta
		resourceNamespace, resourceName       string
	)

	klog.Infof("AdmissionReview for Kind=%v, Namespace=%v Name=%v (%v) UID=%v patchOperation=%v UserInfo=%v",
		req.Kind, req.Namespace, req.Name, resourceName, req.UID, req.Operation, req.UserInfo)

	// 判断GVK
	switch req.Kind.Kind {
	case "Deployment":
		var deployment appsv1.Deployment
		// 反序列出 deployment
		if err := json.Unmarshal(req.Object.Raw, &deployment); err != nil {
			klog.Errorf("could not unmarshal raw object: %v", err)
			return &v1beta1.AdmissionResponse{
				Result: &metav1.Status{
					Message: err.Error(),
				},
			}
		}
		// 获取资源的信息及标签
		resourceName, resourceNamespace, objectMeta = deployment.Name, deployment.Namespace, &deployment.ObjectMeta
		availableAnnotations = deployment.Annotations
		availableLabels = deployment.Labels
	case "Service":
		var service appsv1.Deployment
		if err := json.Unmarshal(req.Object.Raw, &service); err != nil {
			klog.Errorf("could not unmarshal raw object: %v", err)
			return &v1beta1.AdmissionResponse{
				Result: &metav1.Status{
					Message: err.Error(),
				},
			}
		}
		resourceName, resourceNamespace, objectMeta = service.Name, service.Namespace, &service.ObjectMeta
		availableAnnotations = service.Annotations
		availableLabels = service.Labels
	}
	// 判断是否需要 mutate
	if !MutationRequired(ignoredNamespaces, objectMeta) {
		klog.Infof("Skipping validation for %s/%s due to policy check", resourceNamespace, resourceName)
		return &v1beta1.AdmissionResponse{Allowed: true}
	}

	// 更新注解 添加标签
	// annotations := map[string]string{admissionWebhookAnnotationMutateKey: "mutated"}
	addAnnotations[admissionWebhookAnnotationMutateKey] = "mutated"
	addAnnotations[admissionWebhookAnnotationMutateTimeKey] = time.Now().Format(time.RFC3339)
	patchBytes, err := createPatch(availableAnnotations, addAnnotations, availableLabels, addLabels)
	if err != nil {
		return &v1beta1.AdmissionResponse{
			Result: &metav1.Status{
				Message: err.Error(),
			},
		}
	}
	klog.Infof("AdmissionResponse: patch=%v\n", string(patchBytes))
	return &v1beta1.AdmissionResponse{
		Allowed: true,
		Patch:   patchBytes,
		PatchType: func() *v1beta1.PatchType { // PatchType 为指针类型 且 v1beta1.PatchTypeJSONPatch 为常量, 需要封装下
			pt := v1beta1.PatchTypeJSONPatch
			return &pt
		}(),
	}
}

func (whsvr WebhookServer) serve(w http.ResponseWriter, r *http.Request) {
	var body []byte
	if r.Body != nil {
		if data, err := ioutil.ReadAll(r.Body); err == nil {
			body = data
		}
	}
	if len(body) == 0 {
		klog.Error("empty body")
		http.Error(w, "empty body", http.StatusBadRequest)
		return
	}

	// verify the content type is accurate
	contentType := r.Header.Get("Content-Type")
	if contentType != "application/json" {
		klog.Errorf("Content-type=%s, expect application/json", contentType)
		http.Error(w, "invalid Content-Type, expect `application/json`", http.StatusBadRequest)
		return
	}

	var admissionResponse *v1beta1.AdmissionResponse
	ar := v1beta1.AdmissionReview{}
	if _, _, err := deserializer.Decode(body, nil, &ar); err != nil {
		klog.Errorf("Can't decode body: %v", err)
		admissionResponse = &v1beta1.AdmissionResponse{
			Result: &metav1.Status{
				Message: err.Error(),
			},
		}
	} else {
		klog.Infof("######### %v ##########", r.URL.Path)
		if r.URL.Path == "/mutate" {
			admissionResponse = whsvr.mutate(&ar)
		} else if r.URL.Path == "/validate" {
			admissionResponse = whsvr.validate(&ar)
		}
	}

	admissionReview := v1beta1.AdmissionReview{}
	if admissionResponse != nil {
		admissionReview.Response = admissionResponse
		if ar.Request != nil {
			admissionReview.Response.UID = ar.Request.UID
		}
	}

	resp, err := json.Marshal(admissionReview)
	if err != nil {
		klog.Errorf("Can't encode response: %v", err)
		http.Error(w, fmt.Sprintf("could not encode response: %v", err), http.StatusInternalServerError)
	}
	klog.Info("Ready to write response...")
	if _, err := w.Write(resp); err != nil {
		klog.Error("Can't write response: %v", err)
		http.Error(w, fmt.Sprintf("could not write response: %v", err), http.StatusInternalServerError)
	}
}

// https://github.com/kubernetes/kubernetes/issues/72663
func escapeJSONPointerValue(in string) string {
	out := strings.Replace(in, "~", "~0", -1)
	return strings.Replace(out, "/", "~1", -1)
}
