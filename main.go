package main

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/munnerz/kube-plex/pkg/signals"
	"golang.org/x/net/context"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// data pvc name
var dataPVC = os.Getenv("DATA_PVC")

// config pvc name
var configPVC = os.Getenv("CONFIG_PVC")

// transcode pvc name
var transcodePVC = os.Getenv("TRANSCODE_PVC")

// pms namespace
var namespace = os.Getenv("KUBE_NAMESPACE")

// image for the plexmediaserver container containing the transcoder. This
// should be set to the same as the 'master' pms server
var pmsImage = os.Getenv("PMS_IMAGE")
var pmsInternalAddress = os.Getenv("PMS_INTERNAL_ADDRESS")

// the info about where the plex claim secret is held
// instead of passing the claim in plain text to the pods, we pass the secret
var plexClaimSecretName = os.Getenv("PLEX_CLAIM_SECRET_NAME")
var plexClaimSecretKey = os.Getenv("PLEX_CLAIM_SECRET_KEY")

func main() {
	env := os.Environ()
	args := os.Args

	env = rewriteEnv(env)
	rewriteArgs(args)
	cwd, err := os.Getwd()
	if err != nil {
		log.Fatalf("Error getting working directory: %s", err)
	}
	pod := generatePod(cwd, env, args)

	cfg, err := clientcmd.BuildConfigFromFlags("", "")
	if err != nil {
		log.Fatalf("Error building kubeconfig: %s", err)
	}

	kubeClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		log.Fatalf("Error building kubernetes clientset: %s", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pod, err = kubeClient.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		log.Fatalf("Error creating pod: %s", err)
	}

	stopCh := signals.SetupSignalHandler()
	waitFn := func() <-chan error {
		stopCh := make(chan error)
		go func() {
			stopCh <- waitForPodCompletion(kubeClient, pod)
		}()
		return stopCh
	}

	select {
	case err := <-waitFn():
		if err != nil {
			log.Printf("Error waiting for pod to complete: %s", err)
		}
	case <-stopCh:
		log.Printf("Exit requested.")
	}

	log.Printf("Cleaning up pod...")
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	err = kubeClient.CoreV1().Pods(namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{})
	if err != nil {
		log.Fatalf("Error cleaning up pod: %s", err)
	}
}

func removeValFromEnvSlice(s []string, r string) []string {
	for i, v := range s {
		splitvar := strings.SplitN(v, "=", 2)
		if splitvar[0] == r {
			return append(s[:i], s[i+1:]...)
		}
	}
	return s
}

// rewriteEnv rewrites environment variables to be passed to the transcoder
func rewriteEnv(in []string) []string {
	in = removeValFromEnvSlice(in, "PLEX_CLAIM")
	return in
}

func rewriteArgs(in []string) {
	for i, v := range in {
		switch v {
		case "-progressurl", "-manifest_name", "-segment_list":
			in[i+1] = strings.Replace(in[i+1], "http://127.0.0.1:32400", pmsInternalAddress, 1)
		case "-loglevel", "-loglevel_plex":
			in[i+1] = "debug"
		}
	}
}

func generatePod(cwd string, env []string, args []string) *corev1.Pod {
	envVars := toCoreV1EnvVar(env)
	envVars = addFromSecretsToCoreV1EnvVar(envVars)
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "pms-elastic-transcoder-",
		},
		Spec: corev1.PodSpec{
			NodeSelector: map[string]string{
				"beta.kubernetes.io/arch": "amd64",
			},
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{
					Name:       "plex",
					Command:    args,
					Image:      pmsImage,
					Env:        envVars,
					WorkingDir: cwd,
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "data",
							MountPath: "/data",
							ReadOnly:  true,
						},
						{
							Name:      "config",
							MountPath: "/config",
							ReadOnly:  true,
						},
						{
							Name:      "transcode",
							MountPath: "/transcode",
						},
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "data",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: dataPVC,
						},
					},
				},
				{
					Name: "config",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: configPVC,
						},
					},
				},
				{
					Name: "transcode",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: transcodePVC,
						},
					},
				},
			},
		},
	}
}

func addFromSecretsToCoreV1EnvVar(vars []corev1.EnvVar) []corev1.EnvVar {
	secrets := []corev1.EnvVar{
		{
			Name: "PLEX_CLAIM",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: plexClaimSecretName,
					},
					Key: plexClaimSecretKey,
				},
			},
		},
	}
	return append(vars, secrets...)
}

func toCoreV1EnvVar(in []string) []corev1.EnvVar {
	out := make([]corev1.EnvVar, len(in))
	for i, v := range in {
		splitvar := strings.SplitN(v, "=", 2)
		out[i] = corev1.EnvVar{
			Name:  splitvar[0],
			Value: splitvar[1],
		}
	}
	return out
}

func waitForPodCompletion(cl kubernetes.Interface, pod *corev1.Pod) error {
	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		pod, err := cl.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}

		switch pod.Status.Phase {
		case corev1.PodPending:
		case corev1.PodRunning:
		case corev1.PodUnknown:
			log.Printf("Warning: pod %q is in an unknown state", pod.Name)
		case corev1.PodFailed:
			return fmt.Errorf("pod %q failed", pod.Name)
		case corev1.PodSucceeded:
			return nil
		}
		time.Sleep(1 * time.Second)
	}
}
