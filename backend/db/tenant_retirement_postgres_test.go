package db

import "testing"

func TestPostgresTenantLifecycleGuardsPreserveActiveTraining(t *testing.T) {
	database := openPostgresTestSchema(t)
	if err := ApplyMigrations(database); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`INSERT INTO tenants(id,name,namespace,local_queue) VALUES ('live','live','live','live'),('old','old','old','old')`,
		`INSERT INTO users(id,oidc_subject,username,tenant_id) VALUES ('live-user','live-user','live-user','live'),('old-user','old-user','old-user','old')`,
		`INSERT INTO training_jobs(id,tenant_id,user_id,name,spec_json,kubernetes_ns,observed_state) VALUES ('live-job','live','live-user','live-job','{}','live','RUNNING'),('old-job','old','old-user','old-job','{}','old','SUCCEEDED')`,
		`UPDATE tenants SET retired_at=NOW(), retired_by='administrator' WHERE id='old'`,
		`UPDATE training_jobs SET status_message='still running' WHERE id='live-job'`,
		`INSERT INTO job_events(job_id,event_type,component,message) VALUES ('live-job','progress','worker','still running')`,
	} {
		if err := database.Exec(sql).Error; err != nil {
			t.Fatalf("active workload blocked: %v", err)
		}
	}
	for _, sql := range []string{
		`INSERT INTO job_events(job_id,event_type,component,message) VALUES ('old-job','progress','worker','late')`,
		`INSERT INTO job_artifacts(id,job_id,tenant_id,kind,uri) VALUES ('spoofed','old-job','live','checkpoint','test')`,
		`UPDATE training_jobs SET status_message='late' WHERE id='old-job'`,
		`UPDATE tenants SET retired_at=NULL,retired_by='' WHERE id='old'`,
	} {
		if err := database.Exec(sql).Error; err == nil {
			t.Fatalf("retired write accepted: %s", sql)
		}
	}
}
