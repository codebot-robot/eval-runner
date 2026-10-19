// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sinkconfig

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gke-labs/eval-runner/pkg/artifacts"
	"github.com/gke-labs/eval-runner/pkg/artifacts/gcs"
	"github.com/gke-labs/eval-runner/pkg/configkeys"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type errorReader struct {
	err error
}

func (e *errorReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	return e.err
}

func (e *errorReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	return e.err
}

func TestNewArtifactSink(t *testing.T) {
	ctx := t.Context()

	t.Run("empty bucket and dir returns nil without error", func(t *testing.T) {
		sink, err := NewArtifactSink(ctx, "", "", 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if sink != nil {
			t.Fatalf("expected nil sink, got %T", sink)
		}
	})

	t.Run("disabled sentinel returns nil without error", func(t *testing.T) {
		sink, err := NewArtifactSink(ctx, "none", "none", 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if sink != nil {
			t.Fatalf("expected nil sink, got %T", sink)
		}
	})

	t.Run("local directory sink", func(t *testing.T) {
		dir := t.TempDir()
		sink, err := NewArtifactSink(ctx, "", dir, 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		localSink, ok := sink.(*artifacts.LocalDirSink)
		if !ok {
			t.Fatalf("expected *artifacts.LocalDirSink, got %T", sink)
		}
		if localSink.BaseDir != dir {
			t.Errorf("expected BaseDir %q, got %q", dir, localSink.BaseDir)
		}
	})

	t.Run("gcs bucket sink", func(t *testing.T) {
		sink, err := NewArtifactSink(ctx, "my-eval-bucket", "", 1024*1024)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		gcsSink, ok := sink.(*gcs.GCSSink)
		if !ok {
			t.Fatalf("expected *gcs.Sink, got %T", sink)
		}
		if prefix := gcsSink.ArtifactPrefix("abc"); prefix != "gs://my-eval-bucket/runs/abc/" {
			t.Errorf("expected prefix gs://my-eval-bucket/runs/abc/, got %q", prefix)
		}
		if maxUpload := gcsSink.MaxUploadBytes(); maxUpload != 1024*1024 {
			t.Errorf("expected max upload bytes 1048576, got %d", maxUpload)
		}
	})

	t.Run("invalid gcs bucket returns error", func(t *testing.T) {
		sink, err := NewArtifactSink(ctx, "invalid bucket name with spaces", "", 0)
		if err == nil {
			t.Fatalf("expected error for invalid bucket, got sink: %v", sink)
		}
	})
}

func TestConfig_NewArtifactSink(t *testing.T) {
	ctx := t.Context()
	cfg := Config{
		ArtifactDir: t.TempDir(),
	}
	sink, err := cfg.NewArtifactSink(ctx, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := sink.(*artifacts.LocalDirSink); !ok {
		t.Fatalf("expected *artifacts.LocalDirSink, got %T", sink)
	}
}

func TestLoadConfigMap(t *testing.T) {
	ctx := t.Context()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	t.Run("nil reader returns nil", func(t *testing.T) {
		cm, err := LoadConfigMap(ctx, nil, "eval-runner", "eval-runner-config")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cm != nil {
			t.Fatalf("expected nil configmap, got %v", cm)
		}
	})

	t.Run("not found returns nil without error", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).Build()
		cm, err := LoadConfigMap(ctx, cl, "eval-runner", "eval-runner-config")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cm != nil {
			t.Fatalf("expected nil configmap, got %v", cm)
		}
	})

	t.Run("reader error propagates", func(t *testing.T) {
		errReader := &errorReader{err: errors.New("cluster unreachable")}
		cm, err := LoadConfigMap(ctx, errReader, "eval-runner", "eval-runner-config")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "cluster unreachable") {
			t.Errorf("expected error containing cluster unreachable, got %v", err)
		}
		if cm != nil {
			t.Fatalf("expected nil configmap on error, got %v", cm)
		}
	})

	t.Run("success with default namespace and name", func(t *testing.T) {
		existingCM := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: DefaultNamespace,
				Name:      DefaultConfigMapName,
			},
			Data: map[string]string{
				configkeys.ConfigMapKeyArtifactBucket: "gs://my-bucket",
			},
		}
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existingCM).Build()
		cm, err := LoadConfigMap(ctx, cl, "", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cm == nil {
			t.Fatal("expected configmap, got nil")
		}
		if cm.Data[configkeys.ConfigMapKeyArtifactBucket] != "gs://my-bucket" {
			t.Errorf("expected bucket gs://my-bucket, got %q", cm.Data[configkeys.ConfigMapKeyArtifactBucket])
		}
	})
}

func TestLoadArtifactConfig(t *testing.T) {
	ctx := t.Context()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	t.Run("nil reader returns empty config", func(t *testing.T) {
		cfg, err := LoadArtifactConfig(ctx, nil, "eval-runner", "eval-runner-config")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.ArtifactBucket != "" || cfg.ArtifactDir != "" {
			t.Fatalf("expected empty config, got %+v", cfg)
		}
	})

	t.Run("not found returns empty config without error", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).Build()
		cfg, err := LoadArtifactConfig(ctx, cl, "eval-runner", "eval-runner-config")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.ArtifactBucket != "" || cfg.ArtifactDir != "" {
			t.Fatalf("expected empty config, got %+v", cfg)
		}
	})

	t.Run("reader error propagates", func(t *testing.T) {
		errReader := &errorReader{err: errors.New("timeout")}
		_, err := LoadArtifactConfig(ctx, errReader, "eval-runner", "eval-runner-config")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "timeout") {
			t.Errorf("expected error containing timeout, got %v", err)
		}
	})

	t.Run("populates both bucket and dir", func(t *testing.T) {
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "test-ns",
				Name:      "test-cm",
			},
			Data: map[string]string{
				configkeys.ConfigMapKeyArtifactBucket: "gs://eval-bucket",
				configkeys.ConfigMapKeyArtifactDir:    "/var/artifacts",
			},
		}
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm).Build()
		cfg, err := LoadArtifactConfig(ctx, cl, "test-ns", "test-cm")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.ArtifactBucket != "gs://eval-bucket" {
			t.Errorf("expected ArtifactBucket gs://eval-bucket, got %q", cfg.ArtifactBucket)
		}
		if cfg.ArtifactDir != "/var/artifacts" {
			t.Errorf("expected ArtifactDir /var/artifacts, got %q", cfg.ArtifactDir)
		}
	})

	t.Run("ignores disabled sentinels", func(t *testing.T) {
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: DefaultNamespace,
				Name:      DefaultConfigMapName,
			},
			Data: map[string]string{
				configkeys.ConfigMapKeyArtifactBucket: "none",
				configkeys.ConfigMapKeyArtifactDir:    "NONE",
			},
		}
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm).Build()
		cfg, err := LoadArtifactConfig(ctx, cl, "", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.ArtifactBucket != "" {
			t.Errorf("expected empty ArtifactBucket for 'none', got %q", cfg.ArtifactBucket)
		}
		if cfg.ArtifactDir != "" {
			t.Errorf("expected empty ArtifactDir for 'none', got %q", cfg.ArtifactDir)
		}
	})
}
