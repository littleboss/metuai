package egress

import "testing"

func TestConfigReady(t *testing.T) {
	cfg := Config{
		LiveKitURL:       "ws://127.0.0.1:17880",
		LiveKitAPIKey:    "devkey",
		LiveKitAPISecret: "secret",
		S3Endpoint:       "http://127.0.0.1:19000",
		S3Bucket:         "metuai-media",
		S3AccessKey:      "metuai",
		S3SecretKey:      "metuai-secret",
	}
	if !cfg.Ready() {
		t.Fatal("expected ready config")
	}
	cfg.S3Bucket = ""
	if cfg.Ready() {
		t.Fatal("missing bucket should not be ready")
	}
}

func TestPlanObjectKeys(t *testing.T) {
	plans := PlanObjectKeys("mtg_1", "metuai-media", DefaultDesiredOutputs())
	if len(plans) != 3 {
		t.Fatalf("want 3 plans, got %+v", plans)
	}
}

func TestUsesLoopbackS3(t *testing.T) {
	loop := Config{S3Endpoint: "http://127.0.0.1:19000"}
	if !loop.UsesLoopbackS3() {
		t.Fatal("127.0.0.1 must be flagged")
	}
	ok := Config{S3Endpoint: "http://minio:9000"}
	if ok.UsesLoopbackS3() {
		t.Fatal("compose DNS endpoint must not be flagged")
	}
}
