// Command seeddemo resets the portal database and populates a coherent demo
// dataset across every surface — accounts, clusters (with live status), tenants,
// vend orders (with phase timelines), workspaces + runs + state, pipelines,
// templates, operations, org variables, teams, users, and an audit trail.
//
// It targets the same "default" org the dev login lands on (slug "default",
// owner dev@portal.local), so after `task dev` you log in with Dev Login and see
// everything populated — no clicking through empty states.
//
// It TRUNCATEs all tables first, so it's idempotent (a clean reset to the demo
// state on every run) and refuses to run outside ENVIRONMENT=development.
//
//	task seed:demo        # reset + populate
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"

	"github.com/nanohype/portal/internal/config"
	"github.com/nanohype/portal/internal/secrets"
)

func main() {
	if err := run(); err != nil {
		slog.Error("seed failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg := &config.Config{}
	if err := env.Parse(cfg); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	if cfg.Environment != "development" {
		return fmt.Errorf("refusing to run with ENVIRONMENT=%q — seeddemo wipes the database and is dev-only", cfg.Environment)
	}

	enc, err := secrets.NewEncryptor(cfg.EncryptionKey)
	if err != nil {
		return fmt.Errorf("encryptor: %w", err)
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping (is Postgres up + migrated? `docker compose up -d postgres && task db:migrate`): %w", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	s := &seeder{ctx: ctx, tx: tx, enc: enc, now: time.Now().UTC()}
	s.wipe()
	s.seed()
	if s.err != nil {
		return s.err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	fmt.Println("\n✓ demo data seeded into the \"default\" org.")
	fmt.Println("  Start the stack (`task dev`), open http://localhost:5173, and click Dev Login.")
	return nil
}

type seeder struct {
	ctx context.Context
	tx  pgx.Tx
	enc *secrets.Encryptor
	now time.Time
	err error
}

// ── helpers ─────────────────────────────────────────────────────────────────

func id() string { return ulid.Make().String() }

func jb(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func (s *seeder) ago(d time.Duration) time.Time { return s.now.Add(-d) }

func (s *seeder) enc8(plaintext string) string {
	ct, err := s.enc.Encrypt(plaintext)
	if err != nil {
		s.fail("encrypt", err)
		return ""
	}
	return ct
}

// ins runs a parameterized INSERT from a column→value map. cols, placeholders,
// and values are built in one pass so map order doesn't matter.
func (s *seeder) ins(table string, row map[string]any) {
	if s.err != nil {
		return
	}
	cols := make([]string, 0, len(row))
	ph := make([]string, 0, len(row))
	vals := make([]any, 0, len(row))
	i := 1
	for k, v := range row {
		cols = append(cols, k)
		ph = append(ph, fmt.Sprintf("$%d", i))
		vals = append(vals, v)
		i++
	}
	q := "INSERT INTO " + table + " (" + join(cols, ", ") + ") VALUES (" + join(ph, ", ") + ")"
	if _, err := s.tx.Exec(s.ctx, q, vals...); err != nil {
		s.fail(table, err)
	}
}

func (s *seeder) exec(q string, args ...any) {
	if s.err != nil {
		return
	}
	if _, err := s.tx.Exec(s.ctx, q, args...); err != nil {
		s.fail("exec", err)
	}
}

func (s *seeder) fail(what string, err error) {
	if s.err == nil {
		s.err = fmt.Errorf("%s: %w", what, err)
	}
}

func join(parts []string, sep string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += sep
		}
		out += p
	}
	return out
}

// allTables — truncated (CASCADE) before seeding for a clean idempotent reset.
var allTables = []string{
	"organizations", "users", "teams", "team_members", "workspace_team_access",
	"workspaces", "runs", "state_versions", "approvals", "audit_logs",
	"workspace_variables", "org_variables", "pipeline_variables",
	"pipelines", "pipeline_stages", "pipeline_runs", "pipeline_run_stages",
	"accounts", "clusters", "cluster_operations", "tenants", "tenant_operations",
	"templates", "tenant_team_access", "template_team_access",
}

func (s *seeder) wipe() {
	fmt.Println("→ wiping existing data (TRUNCATE … CASCADE)")
	s.exec("TRUNCATE " + join(allTables, ", ") + " RESTART IDENTITY CASCADE")
}

// ── the dataset ─────────────────────────────────────────────────────────────

func (s *seeder) seed() {
	h := time.Hour
	m := time.Minute
	d := 24 * time.Hour

	// org ---------------------------------------------------------------------
	orgID := id()
	s.ins("organizations", map[string]any{
		"id": orgID, "name": "Default Organization", "slug": "default",
		"created_at": s.ago(40 * d),
	})

	// users -------------------------------------------------------------------
	type u struct{ id, email, name, role string }
	users := []u{
		{id(), "dev@portal.local", "Dev User", "owner"},
		{id(), "ari@nanohype.dev", "Ari Mensah", "admin"},
		{id(), "sam@nanohype.dev", "Sam Ortiz", "operator"},
		{id(), "jordan@nanohype.dev", "Jordan Park", "operator"},
		{id(), "robin@nanohype.dev", "Robin Diaz", "viewer"},
	}
	for i, x := range users {
		s.ins("users", map[string]any{
			"id": x.id, "org_id": orgID, "email": x.email, "name": x.name,
			"role": x.role, "avatar_url": "",
			"last_login_at": s.ago(time.Duration(i) * 3 * h),
			"created_at":    s.ago(time.Duration(40-i*3) * d),
		})
	}
	dev, admin, op1, op2, viewer := users[0].id, users[1].id, users[2].id, users[3].id, users[4].id

	// teams + members ---------------------------------------------------------
	type tm struct{ id, name, slug string }
	teams := []tm{{id(), "Platform", "platform"}, {id(), "Data Engineering", "data-eng"}, {id(), "Security", "security"}}
	for i, t := range teams {
		s.ins("teams", map[string]any{"id": t.id, "org_id": orgID, "name": t.name, "slug": t.slug, "created_at": s.ago(time.Duration(30-i*2) * d)})
	}
	tPlatform, tData, tSec := teams[0].id, teams[1].id, teams[2].id
	mem := func(team, user, role, identity string) {
		s.ins("team_members", map[string]any{
			"id": id(), "team_id": team, "user_id": user, "role": role,
			"cloud_identity": identity, "created_at": s.ago(20 * d),
		})
	}
	mem(tPlatform, dev, "owner", "arn:aws:iam::111111111111:role/platform-admin")
	mem(tPlatform, admin, "admin", "")
	mem(tPlatform, op1, "operator", "")
	mem(tData, op2, "operator", "arn:aws:iam::222222222222:role/data-eng")
	mem(tData, viewer, "viewer", "")
	mem(tSec, admin, "admin", "")
	mem(tSec, viewer, "viewer", "")

	// accounts ----------------------------------------------------------------
	type acct struct {
		id, name, awsID, region string
	}
	accounts := []acct{
		{id(), "fleet", "111111111111", "us-west-2"},
		{id(), "workload-production", "222222222222", "us-west-2"},
		{id(), "workload-staging", "333333333333", "us-east-1"},
		{id(), "sandbox", "444444444444", "eu-west-1"},
	}
	descs := map[string]string{
		"fleet":               "Hub account — runs the eks-fleet control plane and vends spokes.",
		"workload-production": "Production workloads.",
		"workload-staging":    "Pre-prod staging.",
		"sandbox":             "Throwaway experiments.",
	}
	for i, a := range accounts {
		s.ins("accounts", map[string]any{
			"id": a.id, "org_id": orgID, "name": a.name, "description": descs[a.name],
			"aws_account_id":        a.awsID,
			"assume_role_arn":       fmt.Sprintf("arn:aws:iam::%s:role/portal-cross-account", a.awsID),
			"external_id_encrypted": s.enc8(fmt.Sprintf("ext-%s", a.awsID[:6])),
			"default_region":        a.region,
			"created_by":            admin,
			"created_at":            s.ago(time.Duration(30-i) * d),
		})
	}
	aFleet, aProd, aStaging, aSandbox := accounts[0].id, accounts[1].id, accounts[2].id, accounts[3].id

	// clusters ----------------------------------------------------------------
	caBundle := s.enc8("-----BEGIN CERTIFICATE-----\nMIIDemoFakeCA...\n-----END CERTIFICATE-----")
	emptyTok := s.enc8("")
	type cl struct {
		id, name, account, env, region string
		conn, argoSync, argoHealth     string
		cpStatus, platVer, k8s         string
		nodes                          int
		connErr                        string
		lastSeen                       bool
	}
	clusters := []cl{
		{id(), "hub-eks", aFleet, "production", "us-west-2", "connected", "Synced", "Healthy", "ACTIVE", "eks.6", "1.33", 3, "", true},
		{id(), "prod-eks", aProd, "production", "us-west-2", "connected", "Synced", "Healthy", "ACTIVE", "eks.6", "1.33", 6, "", true},
		{id(), "data-eks", aProd, "production", "us-west-2", "connected", "OutOfSync", "Degraded", "ACTIVE", "eks.6", "1.33", 8, "", true},
		{id(), "staging-eks", aStaging, "staging", "us-east-1", "connecting", "OutOfSync", "Progressing", "UPDATING", "eks.5", "1.32", 4, "", true},
		{id(), "sandbox-eks", aSandbox, "development", "eu-west-1", "failed", "", "", "", "", "", 0, "dial tcp 10.0.0.12:443: i/o timeout", false},
		{id(), "edge-eks", aSandbox, "development", "eu-west-1", "pending", "", "", "", "", "", 0, "", false},
	}
	for i, c := range clusters {
		row := map[string]any{
			"id": c.id, "org_id": orgID, "account_id": c.account, "name": c.name,
			"description": fmt.Sprintf("%s EKS cluster", c.env), "environment": c.env,
			"api_endpoint":        fmt.Sprintf("https://%s.gr7.%s.eks.amazonaws.com", id()[:12], c.region),
			"ca_bundle_encrypted": caBundle, "sa_token_encrypted": emptyTok,
			"region": c.region, "auth_mode": "eks_iam", "eks_cluster_name": c.name,
			"connection_status": c.conn, "connection_error": c.connErr,
			"node_count": c.nodes, "k8s_version": c.k8s,
			"argocd_sync_status": c.argoSync, "argocd_health_status": c.argoHealth,
			"control_plane_status": c.cpStatus, "platform_version": c.platVer,
			"created_by": admin, "created_at": s.ago(time.Duration(25-i*3) * d),
		}
		if c.lastSeen {
			row["last_connected_at"] = s.ago(time.Duration(i)*m + 2*m)
			row["last_health_observed_at"] = s.ago(time.Duration(i)*m + 1*m)
		}
		s.ins("clusters", row)
	}
	cHub, cProd, cData := clusters[0].id, clusters[1].id, clusters[2].id

	// tenants -----------------------------------------------------------------
	tenant := func(cluster, name, phase, persona string, budget int, models []string) {
		s.ins("tenants", map[string]any{
			"id": id(), "org_id": orgID, "cluster_id": cluster, "name": name, "phase": phase,
			"spec": jb(map[string]any{
				"persona": persona, "budgetUsd": budget, "modelFamilies": models,
				"namespace": "tenant-" + name,
			}),
			"status": jb(map[string]any{
				"observedGeneration": 3,
				"conditions": []map[string]any{
					{"type": "Ready", "status": boolStr(phase), "reason": phase},
				},
			}),
			"last_observed_at": s.ago(2 * m), "created_at": s.ago(12 * d),
		})
	}
	tenant(cProd, "competitive-intelligence", "Ready", "analyst", 5000, []string{"anthropic", "nova"})
	tenant(cProd, "slack-knowledge-bot", "Ready", "assistant", 2000, []string{"anthropic"})
	tenant(cProd, "digest-pipeline", "Provisioning", "batch", 1000, []string{"nova"})
	tenant(cProd, "incident-response", "Ready", "responder", 8000, []string{"anthropic"})
	tenant(cData, "data-ingest", "Healthy", "batch", 3000, []string{"nova", "anthropic"})
	tenant(cData, "feature-store", "Degraded", "batch", 1500, []string{"nova"})
	tenant(cHub, "crossbearing", "Active", "audit", 500, []string{"anthropic"})

	// workspaces + runs + state ----------------------------------------------
	type wspec struct {
		id, name, env, source, repo, branch, dir, tofu string
		auto, approval, vcs, locked                    bool
		lastStatus                                     string // latest run status
		resources                                      int
	}
	wss := []wspec{
		{id(), "lz-network", "production", "upload", "", "", "live/aws/workload-production/us-west-2/production/network", "1.11.0", false, false, false, false, "applied", 12},
		{id(), "lz-cluster", "production", "upload", "", "", "live/aws/workload-production/us-west-2/production/cluster", "1.11.0", false, false, false, false, "applied", 47},
		{id(), "lz-cluster-bootstrap", "production", "upload", "", "", "live/aws/workload-production/us-west-2/production/cluster-bootstrap", "1.11.0", false, false, false, false, "applied", 8},
		{id(), "lz-cluster-addons", "production", "upload", "", "", "live/aws/workload-production/us-west-2/production/cluster-addons", "1.11.0", true, false, false, false, "planning", 23},
		{id(), "app-competitive-intelligence", "production", "vcs", "https://github.com/nanohype/competitive-intelligence", "main", "infra", "1.11.0", true, false, true, false, "applied", 15},
		{id(), "app-digest-pipeline", "staging", "vcs", "https://github.com/nanohype/digest-pipeline", "main", "infra", "1.11.0", false, true, true, false, "errored", 0},
		{id(), "fleet-hub", "production", "upload", "", "", "live/aws/fleet/us-west-2/hub/fleet-hub", "1.11.0", false, true, false, true, "applied", 31},
		{id(), "sandbox-playground", "development", "vcs", "https://github.com/nanohype/sandbox", "dev", ".", "1.11.0", false, true, false, false, "awaiting_approval", 5},
	}
	for i, w := range wss {
		row := map[string]any{
			"id": w.id, "org_id": orgID, "name": w.name, "description": "",
			"source": w.source, "repo_url": w.repo, "repo_branch": orDefault(w.branch, "main"),
			"working_dir": w.dir, "tofu_version": w.tofu, "environment": w.env,
			"auto_apply": w.auto, "requires_approval": w.approval, "vcs_trigger_enabled": w.vcs,
			"locked": w.locked, "created_by": dev, "created_at": s.ago(time.Duration(20-i) * d),
		}
		if w.locked {
			row["locked_by"] = dev
		}
		s.ins("workspaces", row)

		// a couple of historical applied runs, then the latest run carrying lastStatus
		var latestRun string
		history := []string{"applied", "applied"}
		for j, st := range history {
			rid := id()
			s.insRun(rid, w.id, orgID, st, op1, s.ago(time.Duration(10-j*2)*d), 4, 1, 0, "")
		}
		latestRun = id()
		errMsg := ""
		if w.lastStatus == "errored" {
			errMsg = "Error: creating IAM Role: AccessDenied: not authorized to perform iam:CreateRole"
		}
		add, chg, del := 0, 0, 0
		switch w.lastStatus {
		case "applied", "planned":
			add, chg = 3, 2
		}
		s.insRun(latestRun, w.id, orgID, w.lastStatus, op1, s.ago(time.Duration(i)*h+30*m), add, chg, del, errMsg)
		s.exec("UPDATE workspaces SET current_run_id=$1 WHERE id=$2", latestRun, w.id)

		// state version (drives resource_count) for anything that has applied
		if w.resources > 0 && w.lastStatus != "errored" {
			s.ins("state_versions", map[string]any{
				"id": id(), "workspace_id": w.id, "org_id": orgID, "run_id": latestRun,
				"serial": 1 + i, "state_url": "s3://portal/state/" + w.name + ".tfstate",
				"resource_count": w.resources, "resource_summary": fmt.Sprintf("%d resources", w.resources),
				"created_at": s.ago(time.Duration(i) * h),
			})
		}

		// awaiting_approval → a pending approval row
		if w.lastStatus == "awaiting_approval" {
			s.ins("approvals", map[string]any{
				"id": id(), "run_id": latestRun, "org_id": orgID, "user_id": admin,
				"status": "pending", "comment": "", "created_at": s.ago(20 * m),
			})
		}
	}
	wNet, wCluster, wBoot, wAddons := wss[0].id, wss[1].id, wss[2].id, wss[3].id

	// pipelines ---------------------------------------------------------------
	s.seedPipeline(orgID, dev, op1, "eks-gitops-prereqs",
		"Landing-zone prereqs: network → cluster → bootstrap → addons",
		[]string{wNet, wCluster, wBoot, wAddons}, s.ago(8*d))
	s.seedPipeline(orgID, dev, op1, "app-rollout",
		"Application infra rollout across environments",
		[]string{wss[4].id, wss[5].id}, s.ago(3*d))

	// templates ---------------------------------------------------------------
	type tpl struct {
		id, name, persona, desc string
		budget                  int
		models, compliance      []string
		overrides               []string
	}
	tpls := []tpl{
		{id(), "marketing-analyst", "analyst", "Marketing analytics persona with Anthropic + Nova.", 5000, []string{"anthropic", "nova"}, []string{"soc2"}, []string{"budgetUsd", "modelFamilies"}},
		{id(), "data-science", "batch", "Batch data-science workloads.", 10000, []string{"nova", "anthropic"}, []string{}, []string{"budgetUsd"}},
		{id(), "security-audit", "audit", "Locked-down audit persona, no overrides.", 1000, []string{"anthropic"}, []string{"soc2", "hipaa"}, []string{}},
	}
	for i, t := range tpls {
		s.ins("templates", map[string]any{
			"id": t.id, "org_id": orgID, "name": t.name, "description": t.desc, "persona": t.persona,
			"default_values":         jb(map[string]any{"persona": t.persona, "budgetUsd": t.budget, "modelFamilies": t.models}),
			"allowed_overrides":      jb(t.overrides),
			"max_budget_usd":         t.budget,
			"allowed_model_families": jb(t.models),
			"required_compliance":    jb(t.compliance),
			"created_by":             admin, "created_at": s.ago(time.Duration(15-i*2) * d),
		})
	}
	tplMarketing := tpls[0].id

	// cluster operations (vend orders) ---------------------------------------
	at := func(t time.Time) map[string]any { return map[string]any{"at": t, "detail": ""} }
	atd := func(t time.Time, detail string) map[string]any { return map[string]any{"at": t, "detail": detail} }

	// completed provision (wired to the live data-eks cluster)
	s.ins("cluster_operations", map[string]any{
		"id": id(), "org_id": orgID, "name": "data-eks", "environment": "production", "team": "data-eng",
		"operation": "provision", "status": "active", "git_commit_sha": id()[:7], "cluster_id": cData,
		"spec_json": jb(clusterSpec("data-eks", "production", "us-west-2", "1.33")),
		"vend_phases": jb(map[string]any{
			"queued": at(s.ago(5 * h)), "committed": at(s.ago(295 * m)),
			"tofu_running": atd(s.ago(285*m), "applying module.eks.aws_eks_cluster.this"),
			"active":       at(s.ago(255 * m)),
		}),
		"created_by": op2, "created_at": s.ago(5 * h), "completed_at": s.ago(255 * m),
	})
	// in-flight provision (rests at building)
	s.ins("cluster_operations", map[string]any{
		"id": id(), "org_id": orgID, "name": "ml-platform-eks", "environment": "production", "team": "data-eng",
		"operation": "provision", "status": "committed", "git_commit_sha": id()[:7],
		"spec_json": jb(clusterSpec("ml-platform-eks", "production", "us-west-2", "1.33")),
		"vend_phases": jb(map[string]any{
			"queued": at(s.ago(25 * m)), "committed": at(s.ago(24 * m)),
			"tofu_running": atd(s.ago(20*m), "applying module.eks.aws_eks_node_group.default"),
		}),
		"created_by": op1, "created_at": s.ago(25 * m),
	})
	// just-placed provision (pending)
	s.ins("cluster_operations", map[string]any{
		"id": id(), "org_id": orgID, "name": "edge-eks", "environment": "development", "team": "platform",
		"operation": "provision", "status": "pending",
		"spec_json":   jb(clusterSpec("edge-eks", "development", "eu-west-1", "1.33")),
		"vend_phases": jb(map[string]any{}),
		"created_by":  dev, "created_at": s.ago(3 * m),
	})
	// failed provision
	s.ins("cluster_operations", map[string]any{
		"id": id(), "org_id": orgID, "name": "broken-eks", "environment": "staging", "team": "platform",
		"operation": "provision", "status": "failed", "git_commit_sha": id()[:7],
		"error":     "tofu apply: error creating IAM Role: AccessDenied",
		"spec_json": jb(clusterSpec("broken-eks", "staging", "us-east-1", "1.33")),
		"vend_phases": jb(map[string]any{
			"queued": at(s.ago(2 * h)), "committed": at(s.ago(118 * m)),
			"failed": atd(s.ago(110*m), "AccessDenied creating IAM role"),
		}),
		"created_by": op1, "created_at": s.ago(2 * h), "completed_at": s.ago(110 * m),
	})
	// completed deprovision
	s.ins("cluster_operations", map[string]any{
		"id": id(), "org_id": orgID, "name": "old-staging-eks", "environment": "staging", "team": "platform",
		"operation": "deprovision", "status": "deprovisioned", "git_commit_sha": id()[:7],
		"spec_json": jb(map[string]any{}),
		"vend_phases": jb(map[string]any{
			"queued": at(s.ago(6 * h)), "committed": at(s.ago(355 * m)),
			"deprovisioning": atd(s.ago(350*m), "tofu destroy"), "deprovisioned": at(s.ago(320 * m)),
		}),
		"created_by": admin, "created_at": s.ago(6 * h), "completed_at": s.ago(320 * m),
	})
	// in-flight deprovision
	s.ins("cluster_operations", map[string]any{
		"id": id(), "org_id": orgID, "name": "sandbox-eks", "environment": "development", "team": "platform",
		"operation": "deprovision", "status": "committed", "git_commit_sha": id()[:7],
		"spec_json": jb(map[string]any{}),
		"vend_phases": jb(map[string]any{
			"queued": at(s.ago(15 * m)), "committed": at(s.ago(14 * m)),
			"deprovisioning": atd(s.ago(10*m), "destroying module.eks ..."),
		}),
		"created_by": dev, "created_at": s.ago(15 * m),
	})

	// tenant operations -------------------------------------------------------
	s.ins("tenant_operations", map[string]any{
		"id": id(), "org_id": orgID, "cluster_id": cProd, "tenant_name": "competitive-intelligence",
		"operation": "create", "status": "committed", "git_commit_sha": id()[:7], "template_id": tplMarketing,
		"values_json": jb(map[string]any{"persona": "analyst", "budgetUsd": 5000}),
		"created_by":  op1, "created_at": s.ago(12 * d), "completed_at": s.ago(12 * d),
	})
	s.ins("tenant_operations", map[string]any{
		"id": id(), "org_id": orgID, "cluster_id": cProd, "tenant_name": "digest-pipeline",
		"operation": "create", "status": "pending",
		"values_json": jb(map[string]any{"persona": "batch", "budgetUsd": 1000}),
		"created_by":  op1, "created_at": s.ago(8 * m),
	})
	s.ins("tenant_operations", map[string]any{
		"id": id(), "org_id": orgID, "cluster_id": cData, "tenant_name": "bad-tenant",
		"operation": "create", "status": "failed",
		"error":       "helm template: budget 50000 exceeds template cap 10000",
		"values_json": jb(map[string]any{"persona": "batch", "budgetUsd": 50000}),
		"created_by":  op2, "created_at": s.ago(90 * m), "completed_at": s.ago(89 * m),
	})
	s.ins("tenant_operations", map[string]any{
		"id": id(), "org_id": orgID, "cluster_id": cProd, "tenant_name": "legacy-bot",
		"operation": "delete", "status": "committed", "git_commit_sha": id()[:7],
		"values_json": jb(map[string]any{}),
		"created_by":  admin, "created_at": s.ago(2 * d), "completed_at": s.ago(2 * d),
	})

	// org variables -----------------------------------------------------------
	orgVar := func(key, val, cat string, sensitive bool, desc string) {
		s.ins("org_variables", map[string]any{
			"id": id(), "org_id": orgID, "key": key, "value": val, "sensitive": sensitive,
			"category": cat, "description": desc, "created_at": s.ago(30 * d),
		})
	}
	orgVar("AWS_PROFILE", "default", "env", false, "AWS profile used by all workspaces")
	orgVar("AWS_REGION", "us-west-2", "env", false, "Region for the AWS provider")
	orgVar("AWS_DEFAULT_REGION", "us-west-2", "env", false, "Region fallback")
	orgVar("TF_LOG", "INFO", "env", false, "OpenTofu log verbosity")
	orgVar("slack_webhook_url", s.enc8("https://hooks.slack.com/services/T000/B000/secret"), "terraform", true, "Notifications webhook (sensitive)")

	// access pivots -----------------------------------------------------------
	s.ins("workspace_team_access", map[string]any{"id": id(), "workspace_id": wss[6].id, "team_id": tPlatform, "role": "operator", "created_at": s.ago(10 * d)})
	s.ins("workspace_team_access", map[string]any{"id": id(), "workspace_id": wss[4].id, "team_id": tData, "role": "operator", "created_at": s.ago(10 * d)})
	s.ins("tenant_team_access", map[string]any{"id": id(), "org_id": orgID, "cluster_id": cData, "tenant_name": "data-ingest", "team_id": tData, "granted_by": admin, "granted_at": s.ago(10 * d)})
	s.ins("tenant_team_access", map[string]any{"id": id(), "org_id": orgID, "cluster_id": cProd, "tenant_name": "competitive-intelligence", "team_id": tPlatform, "granted_by": admin, "granted_at": s.ago(10 * d)})
	s.ins("template_team_access", map[string]any{"id": id(), "org_id": orgID, "template_id": tplMarketing, "team_id": tPlatform, "granted_by": admin, "granted_at": s.ago(10 * d)})

	// audit logs --------------------------------------------------------------
	type al struct {
		user, action, etype, eid string
		before, after            any
		age                      time.Duration
	}
	logs := []al{
		{admin, "account.create", "account", aProd, nil, map[string]any{"name": "workload-production", "aws_account_id": "222222222222"}, 30 * d},
		{admin, "cluster.register", "cluster", cProd, nil, map[string]any{"name": "prod-eks", "region": "us-west-2"}, 22 * d},
		{dev, "workspace.create", "workspace", wNet, nil, map[string]any{"name": "lz-network", "environment": "production"}, 20 * d},
		{op1, "run.apply", "run", wCluster, map[string]any{"status": "planned"}, map[string]any{"status": "applied", "resources_added": 47}, 18 * d},
		{op1, "tenant.create", "tenant", cProd, nil, map[string]any{"name": "competitive-intelligence", "persona": "analyst"}, 12 * d},
		{admin, "template.create", "template", tplMarketing, nil, map[string]any{"name": "marketing-analyst", "max_budget_usd": 5000}, 15 * d},
		{admin, "team.create", "team", tData, nil, map[string]any{"name": "Data Engineering"}, 30 * d},
		{dev, "variable.update", "org_variable", "slack_webhook_url", map[string]any{"value": "***"}, map[string]any{"value": "***"}, 9 * d},
		{op2, "cluster.provision", "cluster_operation", "data-eks", nil, map[string]any{"name": "data-eks", "team": "data-eng"}, 5 * h},
		{op1, "cluster.provision", "cluster_operation", "ml-platform-eks", nil, map[string]any{"name": "ml-platform-eks"}, 25 * m},
		{admin, "user.role_change", "user", op1, map[string]any{"role": "viewer"}, map[string]any{"role": "operator"}, 14 * d},
		{dev, "cluster.deprovision", "cluster_operation", "sandbox-eks", nil, map[string]any{"name": "sandbox-eks"}, 15 * m},
	}
	for _, l := range logs {
		row := map[string]any{
			"id": id(), "org_id": orgID, "user_id": l.user, "action": l.action,
			"entity_type": l.etype, "entity_id": l.eid,
			"after_data": jb(l.after), "ip_address": "10.0.0.42",
			"user_agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)",
			"created_at": s.ago(l.age),
		}
		if l.before != nil {
			row["before_data"] = jb(l.before)
		}
		s.ins("audit_logs", row)
	}

	if s.err == nil {
		fmt.Printf("→ seeded org=default users=%d teams=%d accounts=%d clusters=%d tenants=%d workspaces=%d pipelines=2 templates=%d orders=6 tenant-ops=4 audit=%d\n",
			len(users), len(teams), len(accounts), len(clusters), 7, len(wss), len(tpls), len(logs))
	}
}

// insRun inserts a run, deriving started/finished from the status.
func (s *seeder) insRun(rid, wsID, orgID, status, by string, created time.Time, add, chg, del int, errMsg string) {
	row := map[string]any{
		"id": rid, "workspace_id": wsID, "org_id": orgID, "operation": "apply",
		"status": status, "resources_added": add, "resources_changed": chg, "resources_deleted": del,
		"error_message": errMsg, "commit_sha": id()[:7], "created_by": by, "created_at": created,
	}
	// terminal statuses get started+finished; in-flight ones get started only
	switch status {
	case "applied", "errored", "planned", "cancelled", "discarded":
		row["started_at"] = created.Add(5 * time.Second)
		row["finished_at"] = created.Add(2 * time.Minute)
	case "planning", "applying":
		row["started_at"] = created.Add(5 * time.Second)
	}
	s.ins("runs", row)
}

// seedPipeline creates a pipeline, its stages, and two runs — one completed and
// one in-flight — with per-stage status rows so the run view renders the
// stage timeline.
func (s *seeder) seedPipeline(orgID, by, runBy, name, desc string, wsIDs []string, created time.Time) {
	pid := id()
	s.ins("pipelines", map[string]any{
		"id": pid, "org_id": orgID, "name": name, "description": desc,
		"created_by": by, "created_at": created,
	})
	stageIDs := make([]string, len(wsIDs))
	for i, ws := range wsIDs {
		sid := id()
		stageIDs[i] = sid
		s.ins("pipeline_stages", map[string]any{
			"id": sid, "pipeline_id": pid, "workspace_id": ws, "stage_order": i,
			"auto_apply": true, "on_failure": "stop", "created_at": created,
		})
	}

	// completed run: every stage completed
	s.seedPipelineRun(pid, orgID, runBy, wsIDs, stageIDs, "completed", len(wsIDs), s.ago(2*24*time.Hour),
		func(int) string { return "completed" })

	// in-flight run: first stages done, current running, rest pending
	current := len(wsIDs) - 1
	if current < 1 {
		current = 1
	}
	s.seedPipelineRun(pid, orgID, runBy, wsIDs, stageIDs, "running", current, s.ago(30*time.Minute),
		func(i int) string {
			switch {
			case i < current-1:
				return "completed"
			case i == current-1:
				return "running"
			default:
				return "pending"
			}
		})
}

func (s *seeder) seedPipelineRun(pid, orgID, by string, wsIDs, stageIDs []string, status string, current int, created time.Time, stageStatus func(int) string) {
	prID := id()
	row := map[string]any{
		"id": prID, "pipeline_id": pid, "org_id": orgID, "status": status,
		"current_stage": current, "total_stages": len(wsIDs),
		"created_by": by, "started_at": created, "created_at": created,
	}
	if status == "completed" || status == "errored" || status == "cancelled" {
		row["finished_at"] = created.Add(10 * time.Minute)
	}
	s.ins("pipeline_runs", row)

	for i := range wsIDs {
		st := stageStatus(i)
		stRow := map[string]any{
			"id": id(), "pipeline_run_id": prID, "stage_id": stageIDs[i], "workspace_id": wsIDs[i],
			"stage_order": i, "status": st, "auto_apply": true, "on_failure": "stop",
			"created_at": created,
		}
		switch st {
		case "completed", "errored":
			stRow["started_at"] = created.Add(time.Duration(i) * 2 * time.Minute)
			stRow["finished_at"] = created.Add(time.Duration(i)*2*time.Minute + 90*time.Second)
		case "running", "importing_outputs":
			stRow["started_at"] = created.Add(time.Duration(i) * 2 * time.Minute)
		}
		s.ins("pipeline_run_stages", stRow)
	}
}

// ── small helpers ────────────────────────────────────────────────────────────

func clusterSpec(name, env, region, version string) map[string]any {
	return map[string]any{
		"name": name, "environment": env, "region": region,
		"version": version, "team": "platform", "endpointPublicAccess": true,
	}
}

// boolStr maps a tenant phase to a k8s-style condition status.
func boolStr(phase string) string {
	switch phase {
	case "Ready", "Healthy", "Active":
		return "True"
	case "Degraded":
		return "False"
	default:
		return "Unknown"
	}
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
