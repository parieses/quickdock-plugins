package main

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"

	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"io"
)

// ---- 凭证明文加密（AES-256-GCM）----
// 注意：密钥硬编码仅用于演示。生产环境应使用 DPAPI (Windows) / Keychain (macOS) / env var。

var authEncryptKey = []byte("quickdock-httpclient-authkey-32b!!") // 必须32字节

// encryptAuth 对认证凭据进行 AES-256-GCM 加密，返回 base64 字符串。
func encryptAuth(val string) (string, error) {
	if val == "" {
		return "", nil
	}
	block, err := aes.NewCipher(authEncryptKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nonce, nonce, []byte(val), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// decryptAuth 解密 encryptAuth 生成的密文。
func decryptAuth(encoded string) (string, error) {
	if encoded == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(authEncryptKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonceSize := gcm.NonceSize()
	if len(raw) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}
	nonce, ciphertext := raw[:nonceSize], raw[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

// ---------- 实体结构（JSON 字段保持 camelCase，便于前端直接消费） ----------

type HttpProject struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Headers   string `json:"headers"`
	Sort      int    `json:"sort"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

type HttpEnvironment struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
	Name      string `json:"name"`
	Variables string `json:"variables"`
	Sort      int    `json:"sort"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

type HttpFolder struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
	ParentID  string `json:"parentId"`
	Name      string `json:"name"`
	Sort      int    `json:"sort"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

type HttpDoc struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
	FolderID  string `json:"folderId"`
	Name      string `json:"name"`
	Content   string `json:"content"`
	Sort      int    `json:"sort"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

type ApiRequest struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	ProjectID string `json:"projectId"`
	FolderID  string `json:"folderId"`
	Method    string `json:"method"`
	URL       string `json:"url"`
	Headers   string `json:"headers"`
	Body      string `json:"body"`
	BodyType  string `json:"bodyType"`
	AuthType  string `json:"authType"`
	AuthTokenEnc string `json:"authTokenEnc"` // 加密存储，读取时解密
	AuthTokenRaw string `json:"-"` // 内存中明文（不持久化）
	AuthUser  string `json:"authUser"`
	AuthPass  string `json:"authPass"`
	Sort      int    `json:"sort"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

type HttpRequestHistory struct {
	ID         string `json:"id"`
	ProjectID  string `json:"projectId"`
	Name       string `json:"name"`
	Method     string `json:"method"`
	URL        string `json:"url"`
	Headers    string `json:"headers"`
	Body       string `json:"body"`
	BodyType   string `json:"bodyType"`
	AuthType   string `json:"authType"`
	AuthToken  string `json:"authToken"`
	AuthUser   string `json:"authUser"`
	AuthPass   string `json:"authPass"`
	StatusCode int    `json:"statusCode"`
	OK         bool   `json:"ok"`
	DurationMs int64  `json:"durationMs"`
	Size       int    `json:"size"`
	CreatedTs  int64  `json:"createdTs"`
	CreatedAt  string `json:"createdAt"`
}

// ---------- 连接 ----------

type DB struct {
	mu   sync.Mutex
	conn *sql.DB
	path string
}

func openDB(path string) (*DB, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("创建数据库目录失败: %w", err)
	}
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}
	conn.SetMaxOpenConns(1)
	if _, err := conn.Exec("PRAGMA journal_mode=WAL"); err != nil {
		conn.Close()
		return nil, fmt.Errorf("设置 WAL 失败: %w", err)
	}
	if _, err := conn.Exec("PRAGMA busy_timeout=5000"); err != nil {
		conn.Close()
		return nil, fmt.Errorf("设置 busy_timeout 失败: %w", err)
	}
	db := &DB{conn: conn, path: path}
	if err := db.migrate(); err != nil {
		conn.Close()
		return nil, err
	}
	return db, nil
}

func (d *DB) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.conn.Close()
}

func (d *DB) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS api_requests (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL DEFAULT '',
			project_id TEXT NOT NULL DEFAULT '',
			folder_id TEXT NOT NULL DEFAULT '',
			method TEXT NOT NULL DEFAULT 'GET',
			url TEXT NOT NULL DEFAULT '',
			headers TEXT NOT NULL DEFAULT '{}',
			body TEXT NOT NULL DEFAULT '',
			body_type TEXT NOT NULL DEFAULT 'json',
			auth_type TEXT NOT NULL DEFAULT '',
			auth_token TEXT NOT NULL DEFAULT '',
			auth_user TEXT NOT NULL DEFAULT '',
			auth_pass TEXT NOT NULL DEFAULT '',
			sort INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_api_requests_sort ON api_requests(sort, created_at)`,

		`CREATE TABLE IF NOT EXISTS http_request_history (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL DEFAULT '',
			method TEXT NOT NULL DEFAULT 'GET',
			url TEXT NOT NULL DEFAULT '',
			headers TEXT NOT NULL DEFAULT '{}',
			body TEXT NOT NULL DEFAULT '',
			body_type TEXT NOT NULL DEFAULT 'json',
			auth_type TEXT NOT NULL DEFAULT '',
			auth_token TEXT NOT NULL DEFAULT '',
			auth_user TEXT NOT NULL DEFAULT '',
			auth_pass TEXT NOT NULL DEFAULT '',
			status_code INTEGER NOT NULL DEFAULT 0,
			ok INTEGER NOT NULL DEFAULT 0,
			duration_ms INTEGER NOT NULL DEFAULT 0,
			size INTEGER NOT NULL DEFAULT 0,
			created_ts INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS idx_http_history_project_ts ON http_request_history(project_id, created_ts)`,

		`CREATE TABLE IF NOT EXISTS http_projects (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL DEFAULT '',
			headers TEXT NOT NULL DEFAULT '{}',
			sort INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_http_projects_sort ON http_projects(sort, created_at)`,

		`CREATE TABLE IF NOT EXISTS http_environments (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL DEFAULT '',
			variables TEXT NOT NULL DEFAULT '[]',
			sort INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_http_env_project ON http_environments(project_id, sort, created_at)`,

		`CREATE TABLE IF NOT EXISTS http_folders (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL DEFAULT '',
			parent_id TEXT DEFAULT '',
			name TEXT NOT NULL DEFAULT '',
			sort INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_http_folders_project ON http_folders(project_id, sort, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_http_folders_parent ON http_folders(parent_id, sort, created_at)`,

		`CREATE TABLE IF NOT EXISTS http_docs (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL DEFAULT '',
			folder_id TEXT DEFAULT '',
			name TEXT NOT NULL DEFAULT '',
			content TEXT NOT NULL DEFAULT '',
			sort INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_http_docs_project ON http_docs(project_id, sort, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_http_docs_folder ON http_docs(folder_id, sort, created_at)`,
	}
	for _, s := range stmts {
		if _, err := d.conn.Exec(s); err != nil {
			return fmt.Errorf("建表失败: %w", err)
		}
	}
	return nil
}

// ---------- 辅助 ----------

func nowStr() string { return time.Now().Format(time.RFC3339) }
func newID() string  { return uuid.New().String() }
func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (d *DB) tx(f func(tx *sql.Tx) error) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := f(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) q(sqlStr string, args ...interface{}) (*sql.Rows, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.conn.Query(sqlStr, args...)
}

func (d *DB) e(sqlStr string, args ...interface{}) (sql.Result, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.conn.Exec(sqlStr, args...)
}

func (d *DB) q1(sqlStr string, args ...interface{}) *sql.Row {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.conn.QueryRow(sqlStr, args...)
}

// ---------- 项目 ----------

const httpProjectCols = `id, name, headers, sort, created_at, updated_at`

func (d *DB) ListProjects() ([]HttpProject, error) {
	rows, err := d.q(`SELECT ` + httpProjectCols + ` FROM http_projects ORDER BY sort ASC, created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HttpProject
	for rows.Next() {
		var r HttpProject
		if err := rows.Scan(&r.ID, &r.Name, &r.Headers, &r.Sort, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		if r.Headers == "" {
			r.Headers = "{}"
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (d *DB) GetProject(id string) (*HttpProject, error) {
	row := d.q1(`SELECT ` + httpProjectCols + ` FROM http_projects WHERE id = ?`, id)
	var r HttpProject
	if err := row.Scan(&r.ID, &r.Name, &r.Headers, &r.Sort, &r.CreatedAt, &r.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if r.Headers == "" {
		r.Headers = "{}"
	}
	return &r, nil
}

func (d *DB) CreateProject(r *HttpProject) error {
	if r.ID == "" {
		r.ID = newID()
	}
	if r.CreatedAt == "" {
		r.CreatedAt = nowStr()
	}
	r.UpdatedAt = r.CreatedAt
	if r.Headers == "" {
		r.Headers = "{}"
	}
	_, err := d.e(`INSERT INTO http_projects (id, name, headers, sort, created_at, updated_at) VALUES (?,?,?,?,?,?)`,
		r.ID, r.Name, r.Headers, r.Sort, r.CreatedAt, r.UpdatedAt)
	return err
}

func (d *DB) UpdateProject(r *HttpProject) error {
	r.UpdatedAt = nowStr()
	if r.Headers == "" {
		r.Headers = "{}"
	}
	_, err := d.e(`UPDATE http_projects SET name=?, headers=?, sort=?, updated_at=? WHERE id=?`,
		r.Name, r.Headers, r.Sort, r.UpdatedAt, r.ID)
	return err
}

func (d *DB) DeleteProject(id string) error {
	return d.tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE api_requests SET project_id='', folder_id='' WHERE project_id=?`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM http_environments WHERE project_id=?`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM http_folders WHERE project_id=?`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM http_docs WHERE project_id=?`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM http_projects WHERE id=?`, id); err != nil {
			return err
		}
		return nil
	})
}

// ---------- 环境变量 ----------

const httpEnvCols = `id, project_id, name, variables, sort, created_at, updated_at`

func (d *DB) ListEnvironments(projectID string) ([]HttpEnvironment, error) {
	rows, err := d.q(`SELECT `+httpEnvCols+` FROM http_environments WHERE project_id=? ORDER BY sort ASC, created_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HttpEnvironment
	for rows.Next() {
		var r HttpEnvironment
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.Name, &r.Variables, &r.Sort, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		if r.Variables == "" {
			r.Variables = "[]"
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (d *DB) GetEnvironment(id string) (*HttpEnvironment, error) {
	row := d.q1(`SELECT `+httpEnvCols+` FROM http_environments WHERE id = ?`, id)
	var r HttpEnvironment
	if err := row.Scan(&r.ID, &r.ProjectID, &r.Name, &r.Variables, &r.Sort, &r.CreatedAt, &r.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if r.Variables == "" {
		r.Variables = "[]"
	}
	return &r, nil
}

func (d *DB) CreateEnvironment(r *HttpEnvironment) error {
	if r.ID == "" {
		r.ID = newID()
	}
	if r.CreatedAt == "" {
		r.CreatedAt = nowStr()
	}
	r.UpdatedAt = r.CreatedAt
	if r.Variables == "" {
		r.Variables = "[]"
	}
	_, err := d.e(`INSERT INTO http_environments (id, project_id, name, variables, sort, created_at, updated_at) VALUES (?,?,?,?,?,?,?)`,
		r.ID, r.ProjectID, r.Name, r.Variables, r.Sort, r.CreatedAt, r.UpdatedAt)
	return err
}

func (d *DB) UpdateEnvironment(r *HttpEnvironment) error {
	r.UpdatedAt = nowStr()
	if r.Variables == "" {
		r.Variables = "[]"
	}
	_, err := d.e(`UPDATE http_environments SET name=?, variables=?, sort=?, updated_at=? WHERE id=?`,
		r.Name, r.Variables, r.Sort, r.UpdatedAt, r.ID)
	return err
}

func (d *DB) DeleteEnvironment(id string) error {
	_, err := d.e(`DELETE FROM http_environments WHERE id=?`, id)
	return err
}

// ---------- 目录 ----------

const httpFolderCols = `id, project_id, parent_id, name, sort, created_at, updated_at`

func (d *DB) ListFolders(projectID string) ([]HttpFolder, error) {
	rows, err := d.q(`SELECT `+httpFolderCols+` FROM http_folders WHERE project_id=? ORDER BY sort ASC, created_at ASC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HttpFolder
	for rows.Next() {
		var r HttpFolder
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.ParentID, &r.Name, &r.Sort, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (d *DB) GetFolder(id string) (*HttpFolder, error) {
	row := d.q1(`SELECT `+httpFolderCols+` FROM http_folders WHERE id = ?`, id)
	var r HttpFolder
	if err := row.Scan(&r.ID, &r.ProjectID, &r.ParentID, &r.Name, &r.Sort, &r.CreatedAt, &r.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &r, nil
}

func (d *DB) CreateFolder(r *HttpFolder) error {
	if r.ID == "" {
		r.ID = newID()
	}
	if r.CreatedAt == "" {
		r.CreatedAt = nowStr()
	}
	r.UpdatedAt = r.CreatedAt
	_, err := d.e(`INSERT INTO http_folders (id, project_id, parent_id, name, sort, created_at, updated_at) VALUES (?,?,?,?,?,?,?)`,
		r.ID, r.ProjectID, r.ParentID, r.Name, r.Sort, r.CreatedAt, r.UpdatedAt)
	return err
}

func (d *DB) UpdateFolder(r *HttpFolder) error {
	r.UpdatedAt = nowStr()
	_, err := d.e(`UPDATE http_folders SET parent_id=?, name=?, sort=?, updated_at=? WHERE id=?`,
		r.ParentID, r.Name, r.Sort, r.UpdatedAt, r.ID)
	return err
}

func (d *DB) IsFolderAncestorOf(folderID, candidateParentID string) (bool, error) {
	cur := candidateParentID
	for i := 0; i < 64 && cur != "" && cur != folderID; i++ {
		var pid string
		err := d.q1(`SELECT parent_id FROM http_folders WHERE id = ?`, cur).Scan(&pid)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return false, nil
			}
			return false, err
		}
		if pid == folderID {
			return true, nil
		}
		cur = pid
	}
	return cur == folderID, nil
}

func (d *DB) folderSubtreeIDs(roots []string) ([]string, error) {
	out := append([]string{}, roots...)
	queue := append([]string{}, roots...)
	seen := map[string]bool{}
	for _, r := range roots {
		seen[r] = true
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		rows, err := d.q(`SELECT id FROM http_folders WHERE parent_id = ?`, cur)
		if err != nil {
			return nil, err
		}
		var children []string
		for rows.Next() {
			var cid string
			if err := rows.Scan(&cid); err != nil {
				rows.Close()
				return nil, err
			}
			if !seen[cid] {
				seen[cid] = true
				children = append(children, cid)
			}
		}
		rows.Close()
		out = append(out, children...)
		queue = append(queue, children...)
	}
	return out, nil
}

func (d *DB) deleteFoldersAndRequests(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	ph := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		ph[i] = "?"
		args[i] = id
	}
	q := strings.Join(ph, ",")
	if _, err := d.e(`DELETE FROM api_requests WHERE folder_id IN (`+q+`)`, args...); err != nil {
		return err
	}
	if _, err := d.e(`DELETE FROM http_docs WHERE folder_id IN (`+q+`)`, args...); err != nil {
		return err
	}
	if _, err := d.e(`DELETE FROM http_folders WHERE id IN (`+q+`)`, args...); err != nil {
		return err
	}
	return nil
}

func (d *DB) DeleteFolder(id string) error {
	ids, err := d.folderSubtreeIDs([]string{id})
	if err != nil {
		return err
	}
	return d.deleteFoldersAndRequests(ids)
}

func (d *DB) ReorderFolders(projectID, parentID string, ids []string) error {
	return d.tx(func(tx *sql.Tx) error {
		for i, id := range ids {
			if _, err := tx.Exec(`UPDATE http_folders SET project_id=?, parent_id=?, sort=? WHERE id=?`,
				projectID, parentID, i, id); err != nil {
				return err
			}
		}
		return nil
	})
}

func (d *DB) UpdateFolderSubtreeProject(rootID, projectID string) error {
	return d.tx(func(tx *sql.Tx) error {
		ids, err := d.folderSubtreeIDs([]string{rootID})
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		ph := make([]string, len(ids))
		args := make([]interface{}, 0, len(ids)+1)
		args = append(args, projectID)
		for i, id := range ids {
			ph[i] = "?"
			args = append(args, id)
		}
		q := strings.Join(ph, ",")
		if _, err := tx.Exec(`UPDATE http_folders SET project_id=? WHERE id IN (`+q+`)`, args...); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE api_requests SET project_id=? WHERE folder_id IN (`+q+`)`, args...); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE http_docs SET project_id=? WHERE folder_id IN (`+q+`)`, args...); err != nil {
			return err
		}
		return nil
	})
}

// ---------- 文档 ----------

const httpDocCols = `id, project_id, folder_id, name, content, sort, created_at, updated_at`

func (d *DB) ListDocs(projectID string) ([]HttpDoc, error) {
	rows, err := d.q(`SELECT `+httpDocCols+` FROM http_docs WHERE project_id=? ORDER BY sort ASC, created_at ASC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HttpDoc
	for rows.Next() {
		var r HttpDoc
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.FolderID, &r.Name, &r.Content, &r.Sort, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (d *DB) GetDoc(id string) (*HttpDoc, error) {
	row := d.q1(`SELECT `+httpDocCols+` FROM http_docs WHERE id = ?`, id)
	var r HttpDoc
	if err := row.Scan(&r.ID, &r.ProjectID, &r.FolderID, &r.Name, &r.Content, &r.Sort, &r.CreatedAt, &r.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &r, nil
}

func (d *DB) CreateDoc(r *HttpDoc) error {
	if r.ID == "" {
		r.ID = newID()
	}
	if r.CreatedAt == "" {
		r.CreatedAt = nowStr()
	}
	r.UpdatedAt = r.CreatedAt
	_, err := d.e(`INSERT INTO http_docs (id, project_id, folder_id, name, content, sort, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?)`,
		r.ID, r.ProjectID, r.FolderID, r.Name, r.Content, r.Sort, r.CreatedAt, r.UpdatedAt)
	return err
}

func (d *DB) UpdateDoc(r *HttpDoc) error {
	r.UpdatedAt = nowStr()
	_, err := d.e(`UPDATE http_docs SET name=?, content=?, sort=?, updated_at=? WHERE id=?`,
		r.Name, r.Content, r.Sort, r.UpdatedAt, r.ID)
	return err
}

func (d *DB) DeleteDoc(id string) error {
	_, err := d.e(`DELETE FROM http_docs WHERE id=?`, id)
	return err
}

// ---------- 请求 ----------

const apiRequestCols = `id, name, project_id, folder_id, method, url, headers, body, body_type, auth_type, auth_token, auth_user, auth_pass, sort, created_at, updated_at`

func (d *DB) ListRequests() ([]ApiRequest, error) {
	rows, err := d.q(`SELECT ` + apiRequestCols + ` FROM api_requests ORDER BY sort ASC, created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ApiRequest
	for rows.Next() {
		var r ApiRequest
		if err := rows.Scan(&r.ID, &r.Name, &r.ProjectID, &r.FolderID, &r.Method, &r.URL, &r.Headers, &r.Body,
			&r.BodyType, &r.AuthType, &r.AuthToken, &r.AuthUser, &r.AuthPass, &r.Sort, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		if r.Headers == "" {
			r.Headers = "{}"
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (d *DB) GetRequest(id string) (*ApiRequest, error) {
	row := d.q1(`SELECT `+apiRequestCols+` FROM api_requests WHERE id = ?`, id)
	var r ApiRequest
	if err := row.Scan(&r.ID, &r.Name, &r.ProjectID, &r.FolderID, &r.Method, &r.URL, &r.Headers, &r.Body,
		&r.BodyType, &r.AuthType, &r.AuthToken, &r.AuthUser, &r.AuthPass, &r.Sort, &r.CreatedAt, &r.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if r.Headers == "" {
		r.Headers = "{}"
	}
	return &r, nil
}

func (d *DB) CreateRequest(r *ApiRequest) error {
	if r.ID == "" {
		r.ID = newID()
	}
	if r.CreatedAt == "" {
		r.CreatedAt = nowStr()
	}
	r.UpdatedAt = r.CreatedAt
	if r.Headers == "" {
		r.Headers = "{}"
	}
	_, err := d.e(`INSERT INTO api_requests
		(id, name, project_id, folder_id, method, url, headers, body, body_type, auth_type, auth_token, auth_user, auth_pass, sort, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.Name, r.ProjectID, r.FolderID, r.Method, r.URL, r.Headers, r.Body, r.BodyType,
		r.AuthType, r.AuthToken, r.AuthUser, r.AuthPass, r.Sort, r.CreatedAt, r.UpdatedAt)
	return err
}

func (d *DB) UpdateRequest(r *ApiRequest) error {
	r.UpdatedAt = nowStr()
	if r.Headers == "" {
		r.Headers = "{}"
	}
	_, err := d.e(`UPDATE api_requests SET
		name=?, project_id=?, folder_id=?, method=?, url=?, headers=?, body=?, body_type=?, auth_type=?,
		auth_token=?, auth_user=?, auth_pass=?, sort=?, updated_at=?
		WHERE id=?`,
		r.Name, r.ProjectID, r.FolderID, r.Method, r.URL, r.Headers, r.Body, r.BodyType, r.AuthType,
		r.AuthToken, r.AuthUser, r.AuthPass, r.Sort, r.UpdatedAt, r.ID)
	return err
}

func (d *DB) DeleteRequest(id string) error {
	_, err := d.e(`DELETE FROM api_requests WHERE id = ?`, id)
	return err
}

func (d *DB) ReorderRequests(projectID, folderID string, ids []string) error {
	return d.tx(func(tx *sql.Tx) error {
		for i, id := range ids {
			if _, err := tx.Exec(`UPDATE api_requests SET project_id=?, folder_id=?, sort=? WHERE id=?`,
				projectID, folderID, i, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// ---------- 历史 ----------

const httpHistoryCols = `id, project_id, name, method, url, headers, body, body_type,
	auth_type, auth_token, auth_user, auth_pass,
	status_code, ok, duration_ms, size, created_ts`

const maxHistoryPerProject = 200

func (d *DB) RecordHistory(h *HttpRequestHistory) (*HttpRequestHistory, error) {
	if h.ID == "" {
		h.ID = newID()
	}
	if h.CreatedTs <= 0 {
		h.CreatedTs = time.Now().UnixMilli()
	}
	if h.Headers == "" {
		h.Headers = "{}"
	}
	if _, err := d.e(`INSERT INTO http_request_history
		(id, project_id, name, method, url, headers, body, body_type,
		 auth_type, auth_token, auth_user, auth_pass, status_code, ok, duration_ms, size, created_ts)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		h.ID, h.ProjectID, h.Name, h.Method, h.URL, h.Headers, h.Body, h.BodyType,
		h.AuthType, h.AuthToken, h.AuthUser, h.AuthPass, h.StatusCode, b2i(h.OK), h.DurationMs, h.Size, h.CreatedTs); err != nil {
		return nil, err
	}
	if _, err := d.e(
		`DELETE FROM http_request_history WHERE project_id = ? AND created_ts < (
			SELECT created_ts FROM http_request_history WHERE project_id = ? ORDER BY created_ts DESC LIMIT 1 OFFSET ?)`,
		h.ProjectID, h.ProjectID, maxHistoryPerProject); err != nil {
		return nil, err
	}
	return h, nil
}

func (d *DB) ListHistory(projectID string, limit int) ([]HttpRequestHistory, error) {
	if limit <= 0 {
		limit = maxHistoryPerProject
	}
	var rows *sql.Rows
	var err error
	if projectID == "" {
		rows, err = d.q(`SELECT `+httpHistoryCols+` FROM http_request_history ORDER BY created_ts DESC LIMIT ?`, limit)
	} else {
		rows, err = d.q(`SELECT `+httpHistoryCols+` FROM http_request_history WHERE project_id = ? ORDER BY created_ts DESC LIMIT ?`, projectID, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HttpRequestHistory
	for rows.Next() {
		var h HttpRequestHistory
		var ok int
		if err := rows.Scan(&h.ID, &h.ProjectID, &h.Name, &h.Method, &h.URL, &h.Headers, &h.Body, &h.BodyType,
			&h.AuthType, &h.AuthToken, &h.AuthUser, &h.AuthPass,
			&h.StatusCode, &ok, &h.DurationMs, &h.Size, &h.CreatedTs); err != nil {
			return nil, err
		}
		h.OK = ok != 0
		if h.Headers == "" {
			h.Headers = "{}"
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (d *DB) GetHistory(id string) (*HttpRequestHistory, error) {
	row := d.q1(`SELECT `+httpHistoryCols+` FROM http_request_history WHERE id = ?`, id)
	var h HttpRequestHistory
	var ok int
	if err := row.Scan(&h.ID, &h.ProjectID, &h.Name, &h.Method, &h.URL, &h.Headers, &h.Body, &h.BodyType,
		&h.AuthType, &h.AuthToken, &h.AuthUser, &h.AuthPass,
		&h.StatusCode, &ok, &h.DurationMs, &h.Size, &h.CreatedTs); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	h.OK = ok != 0
	if h.Headers == "" {
		h.Headers = "{}"
	}
	return &h, nil
}

func (d *DB) DeleteHistory(id string) error {
	_, err := d.e(`DELETE FROM http_request_history WHERE id = ?`, id)
	return err
}

func (d *DB) ClearHistory(projectID string) error {
	var err error
	if projectID == "" {
		_, err = d.e(`DELETE FROM http_request_history`)
	} else {
		_, err = d.e(`DELETE FROM http_request_history WHERE project_id = ?`, projectID)
	}
	return err
}

// 注：旧主程序 quickdock.db 的 http_* 数据不再迁移（按需求丢弃老数据，新插件库独立存储）。

