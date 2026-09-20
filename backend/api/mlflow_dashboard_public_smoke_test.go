package api

import (
	"context"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// This test can only write to the disposable, isolated MLflow server. It uses
// the same HTML, AJAX and artifact paths as a browser, without any credentials.
func TestPublicMLflowDashboardRealServerSmoke(t *testing.T) {
	upstream := firstNonEmptyEnv("MLFLOW_NATIVE_SMOKE_UPSTREAM_URL")
	python := firstNonEmptyEnv("MLFLOW_NATIVE_SMOKE_PYTHON")
	if upstream == "" || python == "" {
		t.Skip("isolated MLflow server and Python required")
	}
	if upstream != "http://rtp-native-mlflow-test:5000/mlflow" {
		t.Fatal("only isolated MLflow smoke upstream is allowed")
	}
	h := newMLflowDashboardTestHandler(newFakeMLflowDashboardStore(), time.Now())
	h.mlflowDashboardPublicEnabled = true
	h.mlflowTrackingURL = upstream
	server := httptest.NewServer(mlflowProxyRouter(h))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	output, err := exec.CommandContext(ctx, python, "-c", publicMLflowDashboardSmokePython, server.URL).CombinedOutput()
	if err != nil {
		t.Fatalf("public dashboard smoke: %v %s", err, output)
	}
	t.Log(strings.TrimSpace(string(output)))
}

const publicMLflowDashboardSmokePython = `
import json, re, sys, time, urllib.request
base = sys.argv[1]
def call(path, payload=None, method=None, content_type='application/json'):
    data = payload if isinstance(payload, bytes) else json.dumps(payload).encode() if payload is not None else None
    req = urllib.request.Request(base + path, data=data, method=method)
    req.add_header('Content-Type', content_type)
    req.add_header('Origin', 'https://portal.example.com')
    with urllib.request.urlopen(req, timeout=30) as res:
        assert res.status == 200, res.status
        assert not res.headers.get('Set-Cookie'), 'anonymous page must not issue cookies'
        return res.read()
def api(path, payload=None):
    return json.loads(call('/mlflow/ajax-api/2.0/mlflow/' + path, payload))
html = call('/mlflow/').decode()
assert '<html' in html.lower(), html[:200]
scripts = re.findall(r'<script[^>]+src=["\x27]([^"\x27]+)', html)
assert scripts, 'MLflow JS assets missing'
for path in scripts:
    assert path.startswith('/mlflow/'), path
    assert len(call(path)) > 100, path
exp = api('experiments/create', {'name': 'public-web-smoke-' + str(time.time_ns())})['experiment_id']
run_id = None
try:
    run = api('runs/create', {'experiment_id': exp})['run']['info']
    run_id = run['run_id']
    api('runs/log-parameter', {'run_id': run_id, 'key': 'source', 'value': 'public-web-smoke'})
    api('runs/log-metric', {'run_id': run_id, 'key': 'loss', 'value': 0.2, 'timestamp': int(time.time()*1000), 'step': 1})
    result = api('runs/get?run_id=' + run_id)['run']
    assert result['info']['experiment_id'] == exp
    assert any(x['key'] == 'loss' and x['value'] == 0.2 for x in result['data']['metrics'])
    artifact = run['artifact_uri']
    assert artifact.startswith('mlflow-artifacts:/'), artifact
    path = '/mlflow/api/2.0/mlflow-artifacts/artifacts/' + artifact[len('mlflow-artifacts:/'):] + '/smoke.bin'
    sample = b'\x00public-web-artifact\xff'
    call(path, sample, 'PUT', 'application/octet-stream')
    assert call(path) == sample
    api('runs/update', {'run_id': run_id, 'status': 'FINISHED'})
    print(json.dumps({'html': True, 'js_assets': len(scripts), 'run_read_write': True, 'artifact_bytes_equal': True}))
finally:
    if run_id: api('runs/delete', {'run_id': run_id})
    api('experiments/delete', {'experiment_id': exp})
print('dedicated smoke Run and experiment deleted')
`
