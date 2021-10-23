package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"

	"github.com/ghodss/yaml"
	"github.com/golang/glog"
	"k8s.io/api/admission/v1beta1"
	admissionregistrationv1beta1 "k8s.io/api/admissionregistration/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	v1 "k8s.io/kubernetes/pkg/apis/core/v1"
)

var (
	runtimeScheme = runtime.NewScheme()
	codecs        = serializer.NewCodecFactory(runtimeScheme)
	deserializer  = codecs.UniversalDeserializer()
)

var ignoredNamespaces = []string{
	metav1.NamespaceSystem,
	metav1.NamespacePublic,
}

type WebhookServer struct {
	serverConfig *Config
	server       *http.Server
}

// Webhook Server parameters
type WhSvrParameters struct {
	port                 int    // webhook server port
	certFile             string // path to the x509 certificate for https
	keyFile              string // path to the x509 private key matching `CertFile`
	webhookserverCfgFile string // path to webhook server configuration file
}

type Config struct {
	Containers []corev1.Container `yaml:"containers"`
	Volumes    []corev1.Volume    `yaml:"volumes"`
}

type patchOperation struct {
	Op    string      `json:"op"`
	Path  string      `json:"path"`
	Value interface{} `json:"value,omitempty"`
}

func init() {
	_ = corev1.AddToScheme(runtimeScheme)
	_ = admissionregistrationv1beta1.AddToScheme(runtimeScheme)
	_ = v1.AddToScheme(runtimeScheme)
}

func loadConfig(configFile string) (*Config, error) {
	data, err := ioutil.ReadFile(configFile)
	if err != nil {
		return nil, err
	}
	glog.Infof("New configuration: sha256sum %x", sha256.Sum256(data))

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func isNotebook(ignoredList []string, metadata *metav1.ObjectMeta) bool {

	for _, namespace := range ignoredList {
		if metadata.Namespace == namespace {
			glog.Infof("Skipping mutation for %v because it's in special namespace:%v", metadata.Name, metadata.Namespace)
			return false
		}
	}

	labels := metadata.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}

	notebook_name := labels["notebook-name"]
	if len(notebook_name) > 0 {
		glog.Infof("%s/%s is notebook pod", metadata.Name, metadata.Namespace)
		return true
	} else {
		glog.Infof("Skipping mutation for %s/%s because it's not Notebook Pod.", metadata.Name, metadata.Namespace)
		return false
	}
}

func mutatePodSpec(path string, podSpec corev1.PodSpec) (patches []patchOperation, PodNeedsGPU bool) {
	// we handle initContainers as well to be safe
	PodNeedsGPU = false
	for i := range podSpec.InitContainers {
		//glog.Info("InitContainers")
		p, ContainerNeedsGPU := mutateContainer(fmt.Sprintf("%s/initContainers/%d", path, i), podSpec.InitContainers[i])
		if ContainerNeedsGPU {
			PodNeedsGPU = true
			return
		}
		patches = append(patches, p...)
	}

	for i := range podSpec.Containers {
		//glog.Info("Containers")
		p, ContainerNeedsGPU := mutateContainer(fmt.Sprintf("%s/containers/%d", path, i), podSpec.Containers[i])
		if ContainerNeedsGPU {
			PodNeedsGPU = true
			return
		}
		patches = append(patches, p...)
	}
	return
}

func mutateContainer(path string, con corev1.Container) (p []patchOperation, needsGPU bool) {

	// start by removing all environment variables related to nvidia gpu setting.
	// log.Printf("Envs are: %+v\n", con.Env)

	disallowedEnv := map[string]bool{
		"NVIDIA_VISIBLE_DEVICES":     true,
		"NVIDIA_DRIVER_CAPABILITIES": true,
		"CUDA_VISIBLE_DEVICES":       true,
	}

	// loop over the list backwords to make sure the JSON Patches are applied in the correct order
	for i := len(con.Env) - 1; i >= 0; i-- {
		env := con.Env[i]
		_, disallowed := disallowedEnv[env.Name]
		if strings.HasPrefix(env.Name, "NVIDIA_REQUIRE_") {
			disallowed = true
		}
		if disallowed {
			// add a patch to remove it
			glog.Info(env.Name)
			glog.Info("disallowed")
			p = append(p, patchOperation{
				Op:   "remove",
				Path: fmt.Sprintf("%s/env/%d", path, i),
			})
		}
	}

	// glog.Info("Resources are: %+v\n", con.Resources)
	needsGPU = false
	for name, value := range con.Resources.Limits {
		//glog.Info(name.String())
		if strings.HasPrefix(name.String(), "nvidia.com/") {
			//glog.Info("find nvidia.com")
			//glog.Info("value:", value)
			if value.IsZero() {
				// not actually requesting a GPU
				// remove the entry because they don't want a GPU and we don't want the device pluggin to get it's hands on this Container
				variant := strings.TrimPrefix(name.String(), "nvidia.com/")
				p = append(p, patchOperation{
					Op:   "remove",
					Path: path + "/resources/limits/nvidia.com~1" + variant,
				})

				// delete the requests as well (if present)
				if _, present := con.Resources.Requests[corev1.ResourceName("nvidia.com/"+variant)]; present {
					p = append(p, patchOperation{
						Op:   "remove",
						Path: path + "/resources/requests/nvidia.com~1" + variant,
					})
				}
			} else {
				needsGPU = true
				break
			}
		}
	}

	if !needsGPU {
		// no GPU requested, let's ensure the GPU is not accessible
		// ensure env is present (otherwise we cannot append to it down below)
		if len(con.Env) == 0 {
			p = append(p, patchOperation{
				Op:    "add",
				Path:  path + "/env",
				Value: []corev1.EnvVar{},
			})
		}

		// We use "none" to allow the libraries to still exist (for runtime purposes) but no GPU
		// "void" is all possible
		// see https://github.com/NVIDIA/nvidia-container-runtime
		p = append(p, patchOperation{
			Op:    "add",
			Path:  path + "/env/-", // add it to the end of env
			Value: corev1.EnvVar{Name: "NVIDIA_VISIBLE_DEVICES", Value: "none", ValueFrom: nil},
		})
	}

	return
}

// main mutation process
func (whsvr *WebhookServer) mutate(ar *v1beta1.AdmissionReview) *v1beta1.AdmissionResponse {
	req := ar.Request
	var pod corev1.Pod
	if err := json.Unmarshal(req.Object.Raw, &pod); err != nil {
		glog.Errorf("Could not unmarshal raw object: %v", err)
		return &v1beta1.AdmissionResponse{
			Result: &metav1.Status{
				Message: err.Error(),
			},
		}
	}

	glog.Infof("AdmissionReview for Kind=%v, Namespace=%v Name=%v (%v) UID=%v patchOperation=%v UserInfo=%v",
		req.Kind, req.Namespace, req.Name, pod.Name, req.UID, req.Operation, req.UserInfo)

	//determine whether this is Notebook Pod
	if !isNotebook(ignoredNamespaces, &pod.ObjectMeta) {
		return &v1beta1.AdmissionResponse{
			Allowed: true,
		}
	}

	patches, PodNeedsGPU := mutatePodSpec("/spec", pod.Spec)
	if PodNeedsGPU {
		glog.Infof("Skipping mutation for %s/%s because this Notebook Pod is requesting GPU", pod.Namespace, pod.Name)
		return &v1beta1.AdmissionResponse{
			Allowed: true,
		}
	}

	glog.Infof("Applying mutation for %s/%s because this Notebook Pod is not requesting GPU", pod.Namespace, pod.Name)

	patchBytes, err := json.Marshal(patches)
	if err != nil {
		return &v1beta1.AdmissionResponse{
			Result: &metav1.Status{
				Message: err.Error(),
			},
		}
	}

	glog.Infof("AdmissionResponse: patch=%v\n", string(patchBytes))
	return &v1beta1.AdmissionResponse{
		Allowed: true,
		Patch:   patchBytes,
		PatchType: func() *v1beta1.PatchType {
			pt := v1beta1.PatchTypeJSONPatch
			return &pt
		}(),
	}
}

// Serve method for webhook server
func (whsvr *WebhookServer) serve(w http.ResponseWriter, r *http.Request) {
	var body []byte
	if r.Body != nil {
		if data, err := ioutil.ReadAll(r.Body); err == nil {
			body = data
		}
	}
	if len(body) == 0 {
		glog.Error("empty body")
		http.Error(w, "empty body", http.StatusBadRequest)
		return
	}

	// verify the content type is accurate
	contentType := r.Header.Get("Content-Type")
	if contentType != "application/json" {
		glog.Errorf("Content-Type=%s, expect application/json", contentType)
		http.Error(w, "invalid Content-Type, expect `application/json`", http.StatusUnsupportedMediaType)
		return
	}

	var admissionResponse *v1beta1.AdmissionResponse
	ar := v1beta1.AdmissionReview{}
	if _, _, err := deserializer.Decode(body, nil, &ar); err != nil {
		glog.Errorf("Can't decode body: %v", err)
		admissionResponse = &v1beta1.AdmissionResponse{
			Result: &metav1.Status{
				Message: err.Error(),
			},
		}
	} else {
		admissionResponse = whsvr.mutate(&ar)
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
		glog.Errorf("Can't encode response: %v", err)
		http.Error(w, fmt.Sprintf("could not encode response: %v", err), http.StatusInternalServerError)
	}
	glog.Infof("Ready to write reponse ...")
	if _, err := w.Write(resp); err != nil {
		glog.Errorf("Can't write response: %v", err)
		http.Error(w, fmt.Sprintf("could not write response: %v", err), http.StatusInternalServerError)
	}
}

// Health check method for webhook server
func (whsvr *WebhookServer) healthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}
