package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/nanohype/portal/internal/auth"
	"github.com/nanohype/portal/internal/handler/respond"
	"github.com/nanohype/portal/internal/repository"
	"github.com/nanohype/portal/internal/service"
)

type AccountHandler struct {
	svc      *service.AccountService
	auditSvc *service.AuditService
}

func NewAccountHandler(svc *service.AccountService, auditSvc *service.AuditService) *AccountHandler {
	return &AccountHandler{svc: svc, auditSvc: auditSvc}
}

type CreateAccountRequest struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	AWSAccountID  string `json:"aws_account_id"`
	AssumeRoleARN string `json:"assume_role_arn"`
	ExternalID    string `json:"external_id"`
	DefaultRegion string `json:"default_region"`
	// Substrate prerequisites landing-zone publishes to SSM for this account, and
	// the vend does not create. Optional: an account that only vends
	// same-account needs none of them, and empty means ungated. A cross-account
	// vend needs the role and both boundaries together — the order is refused
	// otherwise, so registering one without the others buys nothing.
	VendRoleARN                    string `json:"vend_role_arn"`
	DataKmsKeyARN                  string `json:"data_kms_key_arn"`
	ClusterPermissionsBoundaryARN  string `json:"cluster_permissions_boundary_arn"`
	OperatorPermissionsBoundaryARN string `json:"operator_permissions_boundary_arn"`
}

type UpdateAccountRequest struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	AssumeRoleARN string `json:"assume_role_arn"`
	ExternalID    string `json:"external_id"`
	DefaultRegion string `json:"default_region"`
	// Pointers, unlike the fields above: those treat empty as "leave alone",
	// and these have to be clearable. An account that stops vending
	// cross-account must be able to drop its role, and "" is how you say so.
	// Omitting the key leaves the stored value untouched.
	VendRoleARN                    *string `json:"vend_role_arn"`
	DataKmsKeyARN                  *string `json:"data_kms_key_arn"`
	ClusterPermissionsBoundaryARN  *string `json:"cluster_permissions_boundary_arn"`
	OperatorPermissionsBoundaryARN *string `json:"operator_permissions_boundary_arn"`
}

// AccountResponse projects repository.Account for API + audit consumption.
// The ExternalIDEncrypted ciphertext never leaves the server; ExternalIDSet
// lets clients know whether one is configured without seeing it.
type AccountResponse struct {
	ID            string    `json:"id"`
	OrgID         string    `json:"org_id"`
	Name          string    `json:"name"`
	Description   string    `json:"description"`
	AWSAccountID  string    `json:"aws_account_id"`
	AssumeRoleARN string    `json:"assume_role_arn"`
	DefaultRegion string    `json:"default_region"`
	CreatedBy     string    `json:"created_by"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	ExternalIDSet bool      `json:"external_id_set"`
	// Echoed back so the accounts page can show what a vend into this account
	// will be gated by. These are ARNs of resources the account already has, not
	// secrets — unlike the external id, which never leaves the server.
	VendRoleARN                    string `json:"vend_role_arn"`
	DataKmsKeyARN                  string `json:"data_kms_key_arn"`
	ClusterPermissionsBoundaryARN  string `json:"cluster_permissions_boundary_arn"`
	OperatorPermissionsBoundaryARN string `json:"operator_permissions_boundary_arn"`
}

// strDeref reads an optional column as the empty string, which is how the API
// says "not set" for these — they are references, and absent and empty both mean
// ungated.
func strDeref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func accountResponse(a repository.Account) AccountResponse {
	return AccountResponse{
		ID:            a.ID,
		OrgID:         a.OrgID,
		Name:          a.Name,
		Description:   a.Description,
		AWSAccountID:  a.AWSAccountID,
		AssumeRoleARN: a.AssumeRoleARN,
		DefaultRegion: a.DefaultRegion,
		CreatedBy:     a.CreatedBy,
		CreatedAt:     a.CreatedAt,
		UpdatedAt:     a.UpdatedAt,
		ExternalIDSet: a.ExternalIDEncrypted != "",

		VendRoleARN:                    strDeref(a.VendRoleARN),
		DataKmsKeyARN:                  strDeref(a.DataKmsKeyARN),
		ClusterPermissionsBoundaryARN:  strDeref(a.ClusterPermissionsBoundaryARN),
		OperatorPermissionsBoundaryARN: strDeref(a.OperatorPermissionsBoundaryARN),
	}
}

var (
	awsAccountIDRe = regexp.MustCompile(`^\d{12}$`)
	awsRoleARNRe   = regexp.MustCompile(`^arn:aws[a-z-]*:iam::(\d{12}):role/.+$`)
	awsRegionRe    = regexp.MustCompile(`^[a-z]{2}-[a-z]+-\d$`)
)

func isValidAWSAccountID(s string) bool { return awsAccountIDRe.MatchString(s) }
func isValidRoleARN(s string) bool      { return awsRoleARNRe.MatchString(s) }

// substrateARN is a light shape check for the per-account prerequisites. They are
// three different resource types — an IAM role, two IAM policies and a KMS key —
// so this asserts the ARN grammar and a plausible service rather than pretending
// to know each one's path.
var substrateARNRe = regexp.MustCompile(`^arn:aws[a-z-]*:(iam|kms):[a-z0-9-]*:(\d{12}):.+$`)

// checkSubstrateARNs validates the four prerequisites and enforces the one rule
// among them: a vend role is only usable with both permissions boundaries.
//
// clusterspec.Input.Validate refuses that combination too, mirroring the Cluster
// XRD's CEL rule — but it refuses it at order time, which means an account
// registered with a role and no boundaries looks fine until the first
// cross-account vend is rejected. Refusing it here makes the answer free, the
// same trade the order path already makes for an unregistered account and a taken
// cluster name.
func checkSubstrateARNs(vendRole, dataKms, clusterPB, operatorPB string) string {
	for _, f := range []struct{ name, value string }{
		{"vend_role_arn", vendRole},
		{"data_kms_key_arn", dataKms},
		{"cluster_permissions_boundary_arn", clusterPB},
		{"operator_permissions_boundary_arn", operatorPB},
	} {
		if f.value == "" {
			continue
		}
		if len(f.value) > 2048 {
			return f.name + " must be at most 2048 characters"
		}
		if !substrateARNRe.MatchString(f.value) {
			return f.name + " must be an IAM or KMS ARN (arn:aws:<service>:<region>:<account>:<resource>)"
		}
	}
	if vendRole != "" && (clusterPB == "" || operatorPB == "") {
		return "vend_role_arn requires cluster_permissions_boundary_arn and operator_permissions_boundary_arn: " +
			"a cross-account vend mints IAM under the fleet-vend boundary, and an order carrying the role without both is refused"
	}
	return ""
}
func isValidRegion(s string) bool { return awsRegionRe.MatchString(s) }

// accountIDFromARN extracts the 12-digit account number embedded in an IAM
// role ARN, or "" if the ARN doesn't match the expected shape. Used for the
// cross-field check that an assume-role ARN's account matches the
// aws_account_id submitted with it.
func accountIDFromARN(arn string) string {
	m := awsRoleARNRe.FindStringSubmatch(arn)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

func (h *AccountHandler) List(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if perPage < 1 || perPage > 100 {
		perPage = 20
	}

	accounts, total, err := h.svc.List(r.Context(), userCtx.OrgID, page, perPage)
	if err != nil {
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to list accounts")
		return
	}

	data := make([]AccountResponse, len(accounts))
	for i, a := range accounts {
		data[i] = accountResponse(a)
	}

	respond.JSON(w, http.StatusOK, respond.ListResponse[AccountResponse]{
		Data: data, Total: total, Page: page, PerPage: perPage,
	})
}

func (h *AccountHandler) Get(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	accountID := chi.URLParam(r, "accountID")

	account, err := h.svc.Get(r.Context(), accountID, userCtx.OrgID)
	if err != nil {
		respond.FromError(w, r, err)
		return
	}

	respond.JSON(w, http.StatusOK, accountResponse(account))
}

func (h *AccountHandler) Create(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())

	var req CreateAccountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.AWSAccountID = strings.TrimSpace(req.AWSAccountID)
	req.AssumeRoleARN = strings.TrimSpace(req.AssumeRoleARN)
	req.DefaultRegion = strings.TrimSpace(req.DefaultRegion)
	req.VendRoleARN = strings.TrimSpace(req.VendRoleARN)
	req.DataKmsKeyARN = strings.TrimSpace(req.DataKmsKeyARN)
	req.ClusterPermissionsBoundaryARN = strings.TrimSpace(req.ClusterPermissionsBoundaryARN)
	req.OperatorPermissionsBoundaryARN = strings.TrimSpace(req.OperatorPermissionsBoundaryARN)

	if msg := checkSubstrateARNs(req.VendRoleARN, req.DataKmsKeyARN,
		req.ClusterPermissionsBoundaryARN, req.OperatorPermissionsBoundaryARN); msg != "" {
		respond.Error(w, http.StatusBadRequest, msg)
		return
	}

	if req.Name == "" {
		respond.Error(w, http.StatusBadRequest, "name is required")
		return
	}
	if len(req.Name) > 128 {
		respond.Error(w, http.StatusBadRequest, "name must be at most 128 characters")
		return
	}
	if len(req.Description) > 4096 {
		respond.Error(w, http.StatusBadRequest, "description must be at most 4096 characters")
		return
	}
	// AWS wiring is optional. An account with no assume-role ARN is a no-AWS
	// grouping — for local/kind clusters or any cluster portal reaches
	// directly with its own kubeconfig credentials. When an ARN is set it must
	// be well-formed and consistent with a 12-digit account id; absent it, the
	// AWS-only fields are skipped entirely so a name is enough.
	if req.AssumeRoleARN != "" {
		if len(req.AssumeRoleARN) > 2048 {
			respond.Error(w, http.StatusBadRequest, "assume_role_arn must be at most 2048 characters")
			return
		}
		if !isValidRoleARN(req.AssumeRoleARN) {
			respond.Error(w, http.StatusBadRequest, "assume_role_arn must be a valid IAM role ARN (arn:aws:iam::<account>:role/<name>)")
			return
		}
		if !isValidAWSAccountID(req.AWSAccountID) {
			respond.Error(w, http.StatusBadRequest, "aws_account_id (exactly 12 digits) is required when assume_role_arn is set")
			return
		}
		if accountIDFromARN(req.AssumeRoleARN) != req.AWSAccountID {
			respond.Error(w, http.StatusBadRequest, "assume_role_arn account does not match aws_account_id")
			return
		}
	} else if req.AWSAccountID != "" && !isValidAWSAccountID(req.AWSAccountID) {
		respond.Error(w, http.StatusBadRequest, "aws_account_id must be exactly 12 digits")
		return
	}
	if req.DefaultRegion != "" && !isValidRegion(req.DefaultRegion) {
		respond.Error(w, http.StatusBadRequest, "default_region must look like 'us-west-2' (lowercase, hyphenated, ends with a digit)")
		return
	}
	if len(req.ExternalID) > 1024 {
		respond.Error(w, http.StatusBadRequest, "external_id must be at most 1024 characters")
		return
	}

	account, err := h.svc.Create(r.Context(), service.CreateAccountParams{
		OrgID:         userCtx.OrgID,
		Name:          req.Name,
		Description:   req.Description,
		AWSAccountID:  req.AWSAccountID,
		AssumeRoleARN: req.AssumeRoleARN,
		ExternalID:    req.ExternalID,
		DefaultRegion: req.DefaultRegion,

		VendRoleARN:                    req.VendRoleARN,
		DataKmsKeyARN:                  req.DataKmsKeyARN,
		ClusterPermissionsBoundaryARN:  req.ClusterPermissionsBoundaryARN,
		OperatorPermissionsBoundaryARN: req.OperatorPermissionsBoundaryARN,

		CreatedBy: userCtx.UserID,
	})
	if err != nil {
		if isUniqueViolation(err) {
			respond.Error(w, http.StatusConflict, "an account with this name or AWS account ID already exists")
			return
		}
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to create account")
		return
	}

	ip, ua := auditContext(r)
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "account.create", EntityType: "account", EntityID: account.ID,
		After: accountResponse(account), IPAddress: ip, UserAgent: ua,
	})

	respond.JSON(w, http.StatusCreated, accountResponse(account))
}

func (h *AccountHandler) Update(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	accountID := chi.URLParam(r, "accountID")

	existing, err := h.svc.Get(r.Context(), accountID, userCtx.OrgID)
	if err != nil {
		respond.FromError(w, r, err)
		return
	}

	var req UpdateAccountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.AssumeRoleARN = strings.TrimSpace(req.AssumeRoleARN)
	req.DefaultRegion = strings.TrimSpace(req.DefaultRegion)

	if len(req.Name) > 128 {
		respond.Error(w, http.StatusBadRequest, "name must be at most 128 characters")
		return
	}
	if len(req.Description) > 4096 {
		respond.Error(w, http.StatusBadRequest, "description must be at most 4096 characters")
		return
	}
	if len(req.AssumeRoleARN) > 2048 {
		respond.Error(w, http.StatusBadRequest, "assume_role_arn must be at most 2048 characters")
		return
	}
	if req.AssumeRoleARN != "" {
		if !isValidRoleARN(req.AssumeRoleARN) {
			respond.Error(w, http.StatusBadRequest, "assume_role_arn must be a valid IAM role ARN")
			return
		}
		// aws_account_id is immutable, so the new ARN must still point at it.
		if accountIDFromARN(req.AssumeRoleARN) != existing.AWSAccountID {
			respond.Error(w, http.StatusBadRequest, "assume_role_arn account does not match aws_account_id")
			return
		}
	}
	if req.DefaultRegion != "" && !isValidRegion(req.DefaultRegion) {
		respond.Error(w, http.StatusBadRequest, "default_region must look like 'us-west-2'")
		return
	}
	// The substrate rule has to be checked against the merged result, not the
	// request. A patch that clears one permissions boundary sends only that field;
	// checking it alone would pass and leave an account carrying a vend role it can
	// no longer use, which the operator would discover at the next cross-account
	// vend. `merged` is what the row will actually hold.
	merged := func(patch *string, stored *string) string {
		if patch != nil {
			return strings.TrimSpace(*patch)
		}
		return strDeref(stored)
	}
	if msg := checkSubstrateARNs(
		merged(req.VendRoleARN, existing.VendRoleARN),
		merged(req.DataKmsKeyARN, existing.DataKmsKeyARN),
		merged(req.ClusterPermissionsBoundaryARN, existing.ClusterPermissionsBoundaryARN),
		merged(req.OperatorPermissionsBoundaryARN, existing.OperatorPermissionsBoundaryARN),
	); msg != "" {
		respond.Error(w, http.StatusBadRequest, msg)
		return
	}
	// Trim in place so the stored value matches what was validated.
	for _, f := range []**string{
		&req.VendRoleARN, &req.DataKmsKeyARN,
		&req.ClusterPermissionsBoundaryARN, &req.OperatorPermissionsBoundaryARN,
	} {
		if *f != nil {
			t := strings.TrimSpace(**f)
			*f = &t
		}
	}

	if len(req.ExternalID) > 1024 {
		respond.Error(w, http.StatusBadRequest, "external_id must be at most 1024 characters")
		return
	}

	account, err := h.svc.Update(r.Context(), service.UpdateAccountParams{
		ID:            accountID,
		OrgID:         userCtx.OrgID,
		Name:          req.Name,
		Description:   req.Description,
		AssumeRoleARN: req.AssumeRoleARN,
		ExternalID:    req.ExternalID,
		DefaultRegion: req.DefaultRegion,

		VendRoleARN:                    req.VendRoleARN,
		DataKmsKeyARN:                  req.DataKmsKeyARN,
		ClusterPermissionsBoundaryARN:  req.ClusterPermissionsBoundaryARN,
		OperatorPermissionsBoundaryARN: req.OperatorPermissionsBoundaryARN,
	})
	if err != nil {
		if isUniqueViolation(err) {
			respond.Error(w, http.StatusConflict, "an account with this name already exists")
			return
		}
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to update account")
		return
	}

	ip, ua := auditContext(r)
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "account.update", EntityType: "account", EntityID: accountID,
		Before: accountResponse(existing), After: accountResponse(account),
		IPAddress: ip, UserAgent: ua,
	})

	respond.JSON(w, http.StatusOK, accountResponse(account))
}

func (h *AccountHandler) Delete(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	accountID := chi.URLParam(r, "accountID")

	existing, err := h.svc.Get(r.Context(), accountID, userCtx.OrgID)
	if err != nil {
		respond.FromError(w, r, err)
		return
	}

	if err := h.svc.Delete(r.Context(), accountID, userCtx.OrgID); err != nil {
		// Once Cluster lands with an FK to accounts (ON DELETE RESTRICT), a
		// referenced account will surface here as a foreign-key violation.
		if isForeignKeyViolation(err) {
			respond.Error(w, http.StatusConflict, "cannot delete account referenced by other resources")
			return
		}
		respond.ErrorWithRequest(w, r, http.StatusInternalServerError, "failed to delete account")
		return
	}

	ip, ua := auditContext(r)
	h.auditSvc.Log(r.Context(), service.AuditEntry{
		OrgID: userCtx.OrgID, UserID: userCtx.UserID,
		Action: "account.delete", EntityType: "account", EntityID: accountID,
		Before: accountResponse(existing), IPAddress: ip, UserAgent: ua,
	})

	respond.NoContent(w)
}
