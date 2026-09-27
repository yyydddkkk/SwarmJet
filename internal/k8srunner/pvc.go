package k8srunner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func PVCName(taskID string) string { return "sj-task-" + taskID }

func (c *Controller) pvcPath(name string) string {
	path := "/api/v1/namespaces/" + c.namespace + "/persistentvolumeclaims"
	if name != "" {
		path += "/" + name
	}
	return path
}

// EnsurePVC creates a task-scoped workspace. The caller must retain it until
// checkpoints and reports have been persisted to the artifact store.
func (c *Controller) EnsurePVC(ctx context.Context, taskID string, storageMi int64) (string, error) {
	if !validDNS(taskID) || len(PVCName(taskID)) > 63 {
		return "", errors.New("invalid task ID")
	}
	if storageMi < 64 || storageMi > 102400 {
		return "", errors.New("PVC size must be 64..102400 MiB")
	}
	name := PVCName(taskID)
	claim := map[string]any{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": map[string]any{"name": name, "labels": map[string]any{"app.kubernetes.io/name": "swarmjet-runner", "swarmjet.io/task-id": taskID}}, "spec": map[string]any{"accessModes": []string{"ReadWriteOnce"}, "resources": map[string]any{"requests": map[string]any{"storage": strconv.FormatInt(storageMi, 10) + "Mi"}}}}
	var created map[string]any
	status, err := c.request(ctx, http.MethodPost, c.pvcPath(""), claim, &created)
	if status == http.StatusConflict {
		_, err = c.request(ctx, http.MethodGet, c.pvcPath(name), nil, &created)
		if err != nil {
			return "", err
		}
		var got struct {
			Metadata struct{ Labels map[string]string }
			Spec     struct {
				AccessModes []string
				Resources   struct{ Requests map[string]string }
			}
		}
		data, _ := json.Marshal(created)
		if err := json.Unmarshal(data, &got); err != nil {
			return "", err
		}
		if got.Metadata.Labels["swarmjet.io/task-id"] != taskID || len(got.Spec.AccessModes) != 1 || got.Spec.AccessModes[0] != "ReadWriteOnce" || got.Spec.Resources.Requests["storage"] != strconv.FormatInt(storageMi, 10)+"Mi" {
			return "", errors.New("existing PVC conflicts with requested task workspace")
		}
		return name, nil
	}
	if err != nil {
		return "", err
	}
	return name, nil
}

// DeletePVC is explicit cleanup after report and checkpoint retention checks.
func (c *Controller) DeletePVC(ctx context.Context, taskID string) error {
	if !validDNS(taskID) || len(PVCName(taskID)) > 63 {
		return errors.New("invalid task ID")
	}
	name := PVCName(taskID)
	var before map[string]any
	status, err := c.request(ctx, http.MethodGet, c.pvcPath(name), nil, &before)
	if status == http.StatusNotFound {
		return nil
	}
	if err != nil {
		return err
	}
	beforeMeta, _ := before["metadata"].(map[string]any)
	beforeLabels, _ := beforeMeta["labels"].(map[string]any)
	if fmt.Sprint(beforeLabels["swarmjet.io/task-id"]) != taskID {
		return errors.New("PVC is not owned by task")
	}
	status, err = c.request(ctx, http.MethodDelete, c.pvcPath(name), map[string]any{"apiVersion": "v1", "kind": "DeleteOptions", "propagationPolicy": "Foreground"}, nil)
	if status != http.StatusNotFound && err != nil {
		return err
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		var claim map[string]any
		status, err = c.request(ctx, http.MethodGet, c.pvcPath(name), nil, &claim)
		if status == http.StatusNotFound {
			return nil
		}
		if err != nil {
			return fmt.Errorf("wait for PVC deletion: %w", err)
		}
		meta, _ := claim["metadata"].(map[string]any)
		labels, _ := meta["labels"].(map[string]any)
		if strings.TrimSpace(fmt.Sprint(labels["swarmjet.io/task-id"])) != taskID {
			return errors.New("PVC ownership changed during deletion")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
