package main

import (
	"context"
	"encoding/json"
	"sync"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func handleCommand(id int64, p executeParams) {
	cmd := strings.ToLower(strings.TrimSpace(p.Command))
	switch cmd {
	// 请求
	case "req.send":
		cmdReqSend(id, p.Input)
	case "req.list":
		cmdReqList(id)
	case "req.save":
		cmdReqSave(id, p.Input)
	case "req.get":
		cmdReqGet(id, p.Input)
	case "req.delete":
		cmdReqDelete(id, p.Input)
	case "req.curl":
		cmdReqCurl(id, p.Input)

	// 历史
	case "history.list":
		cmdHistoryList(id, p.Input)
	case "history.clear":
		cmdHistoryClear(id, p.Input)
	case "history.delete":
		cmdHistoryDelete(id, p.Input)

	// 项目
	case "project.list":
		cmdProjectList(id)
	case "project.save":
		cmdProjectSave(id, p.Input)
	case "project.delete":
		cmdProjectDelete(id, p.Input)

	// 目录
	case "folder.list":
		cmdFolderList(id, p.Input)
	case "folder.save":
		cmdFolderSave(id, p.Input)
	case "folder.delete":
		cmdFolderDelete(id, p.Input)
	case "folder.reorder":
		cmdFolderReorder(id, p.Input)

	// 文档
	case "doc.list":
		cmdDocList(id, p.Input)
	case "doc.save":
		cmdDocSave(id, p.Input)
	case "doc.get":
		cmdDocGet(id, p.Input)
	case "doc.delete":
		cmdDocDelete(id, p.Input)

	// 环境
	case "env.list":
		cmdEnvList(id, p.Input)
	case "env.save":
		cmdEnvSave(id, p.Input)
	case "env.delete":
		cmdEnvDelete(id, p.Input)
	case "env.resolve":
		cmdEnvResolve(id, p.Input)

	// 导入
	case "project.importpostman", "project.importPostman":
		cmdImportPostman(id, p.Input)

	default:
		respondError(id, -32601, "unknown command: "+p.Command)
	}
}

// ---------- 请求 ----------

func toApiRequestInput(input map[string]interface{}) ApiRequestInput {
	return ApiRequestInput{
		ID:            strFrom(input, "id"),
		Name:          strFrom(input, "name"),
		ProjectID:     strFrom(input, "projectId"),
		FolderID:      strFrom(input, "folderId"),
		EnvironmentID: strFrom(input, "environmentId"),
		Method:        strFrom(input, "method"),
		URL:           strFrom(input, "url"),
		Headers:       strFrom(input, "headers"),
		Body:          strFrom(input, "body"),
		BodyType:      strFrom(input, "bodyType"),
		AuthType:      strFrom(input, "authType"),
		AuthToken:     strFrom(input, "authToken"),
		AuthUser:      strFrom(input, "authUser"),
		AuthPass:      strFrom(input, "authPass"),
		Sort:          intFrom(input, "sort", 0),
	}
}

func validateApiRequest(input *ApiRequestInput) error {
	input.Method = strings.ToUpper(strings.TrimSpace(input.Method))
	if input.Method == "" {
		input.Method = http.MethodGet
	}
	switch input.Method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete,
		http.MethodPatch, http.MethodHead, http.MethodOptions:
	default:
		return fmt.Errorf("不支持的 HTTP 方法: %s", input.Method)
	}
	input.URL = strings.TrimSpace(input.URL)
	if input.URL == "" {
		return fmt.Errorf("URL 不能为空")
	}
	if input.BodyType == "" {
		input.BodyType = "json"
	}
	if input.Headers != "" {
		var m map[string]string
		if err := json.Unmarshal([]byte(input.Headers), &m); err != nil {
			return fmt.Errorf("Headers 不是合法 JSON: %w", err)
		}
	}
	return nil
}

func cmdReqSend(id int64, input map[string]interface{}) {
	in := toApiRequestInput(input)
	if err := validateApiRequest(&in); err != nil {
		respondError(id, -32603, err.Error())
		return
	}
	cp := in
	if err := srv.applyProjectAndEnv(&cp); err != nil {
		respondError(id, -32603, err.Error())
		return
	}
	resp, err := doUserHTTP(cp)
	if err != nil {
		respondError(id, -32603, err.Error())
		return
	}
ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	go func() {
		_, _ = srv.db.RecordHistory(&HttpRequestHistory{
			ProjectID:  cp.ProjectID,
			Name:       cp.Name,
			Method:     cp.Method,
			URL:        cp.URL,
			Headers:    cp.Headers,
			Body:       cp.Body,
			BodyType:   cp.BodyType,
			AuthType:   cp.AuthType,
			AuthToken:  cp.AuthToken,
			AuthUser:   cp.AuthUser,
			AuthPass:   cp.AuthPass,
			StatusCode: resp.Status,
			OK:         resp.OK,
			DurationMs: resp.DurationMs,
			Size:       resp.Size,
		})
	}()
	respond(id, resp)
}

func cmdReqList(id int64) {
	list, err := srv.db.ListRequests()
	if err != nil {
		respondError(id, -32603, err.Error())
		return
	}
	respond(id, list)
}

func cmdReqSave(id int64, input map[string]interface{}) {
	in := toApiRequestInput(input)
	if r := validateApiRequest(&in); r != nil {
		respondError(id, -32603, r.Error())
		return
	}
	rec := &ApiRequest{
		ID:        in.ID,
		Name:      strings.TrimSpace(in.Name),
		ProjectID: in.ProjectID,
		FolderID:  in.FolderID,
		Method:    in.Method,
		URL:       in.URL,
		Headers:   in.Headers,
		Body:      in.Body,
		BodyType:  in.BodyType,
		AuthType:  in.AuthType,
		AuthToken: in.AuthToken,
		AuthUser:  in.AuthUser,
		AuthPass:  in.AuthPass,
		Sort:      in.Sort,
	}
	if rec.ID == "" {
		if err := srv.db.CreateRequest(rec); err != nil {
			respondError(id, -32603, err.Error())
			return
		}
	} else {
		if err := srv.db.UpdateRequest(rec); err != nil {
			respondError(id, -32603, err.Error())
			return
		}
	}
	respond(id, rec)
}

func cmdReqGet(id int64, input map[string]interface{}) {
	rec, err := srv.db.GetRequest(strFrom(input, "id"))
	if err != nil {
		respondError(id, -32603, err.Error())
		return
	}
	if rec == nil {
		respondError(id, -32603, "请求不存在")
		return
	}
	respond(id, rec)
}

func cmdReqDelete(id int64, input map[string]interface{}) {
	if err := srv.db.DeleteRequest(strFrom(input, "id")); err != nil {
		respondError(id, -32603, err.Error())
		return
	}
	respond(id, map[string]interface{}{"ok": true})
}

func cmdReqCurl(id int64, input map[string]interface{}) {
	in := toApiRequestInput(input)
	if r := validateApiRequest(&in); r != nil {
		respondError(id, -32603, r.Error())
		return
	}
	cp := in
	if err := srv.applyProjectAndEnv(&cp); err != nil {
		respondError(id, -32603, err.Error())
		return
	}
	cmdStr := buildCurlString(ApiRequestInput{
		Method:    cp.Method,
		URL:       cp.URL,
		Headers:   cp.Headers,
		Body:      cp.Body,
		BodyType:  cp.BodyType,
		AuthType:  cp.AuthType,
		AuthToken: cp.AuthToken,
		AuthUser:  cp.AuthUser,
		AuthPass:  cp.AuthPass,
	})
	respond(id, map[string]interface{}{"command": cmdStr})
}

// ---------- 历史 ----------

func cmdHistoryList(id int64, input map[string]interface{}) {
	limit := intFrom(input, "limit", 100)
	list, err := srv.db.ListHistory(strFrom(input, "projectId"), limit)
	if err != nil {
		respondError(id, -32603, err.Error())
		return
	}
	respond(id, list)
}

func cmdHistoryClear(id int64, input map[string]interface{}) {
	if err := srv.db.ClearHistory(strFrom(input, "projectId")); err != nil {
		respondError(id, -32603, err.Error())
		return
	}
	respond(id, map[string]interface{}{"ok": true})
}

func cmdHistoryDelete(id int64, input map[string]interface{}) {
	if err := srv.db.DeleteHistory(strFrom(input, "id")); err != nil {
		respondError(id, -32603, err.Error())
		return
	}
	respond(id, map[string]interface{}{"ok": true})
}

// ---------- 项目 ----------

func cmdProjectList(id int64) {
	list, err := srv.db.ListProjects()
	if err != nil {
		respondError(id, -32603, err.Error())
		return
	}
	respond(id, list)
}

func cmdProjectSave(id int64, input map[string]interface{}) {
	rec := &HttpProject{
		ID:      strFrom(input, "id"),
		Name:    strings.TrimSpace(strFrom(input, "name")),
		Headers: strFrom(input, "headers"),
		Sort:    intFrom(input, "sort", 0),
	}
	if rec.Name == "" {
		rec.Name = "未命名项目"
	}
	if rec.Headers == "" {
		rec.Headers = "{}"
	}
	if rec.ID == "" {
		if err := srv.db.CreateProject(rec); err != nil {
			respondError(id, -32603, err.Error())
			return
		}
	} else {
		if err := srv.db.UpdateProject(rec); err != nil {
			respondError(id, -32603, err.Error())
			return
		}
	}
	respond(id, rec)
}

func cmdProjectDelete(id int64, input map[string]interface{}) {
	if err := srv.db.DeleteProject(strFrom(input, "id")); err != nil {
		respondError(id, -32603, err.Error())
		return
	}
	respond(id, map[string]interface{}{"ok": true})
}

// ---------- 目录 ----------

func cmdFolderList(id int64, input map[string]interface{}) {
	list, err := srv.db.ListFolders(strFrom(input, "projectId"))
	if err != nil {
		respondError(id, -32603, err.Error())
		return
	}
	respond(id, list)
}

func cmdFolderSave(id int64, input map[string]interface{}) {
	rec := &HttpFolder{
		ID:        strFrom(input, "id"),
		ProjectID: strFrom(input, "projectId"),
		ParentID:  strFrom(input, "parentId"),
		Name:      strings.TrimSpace(strFrom(input, "name")),
		Sort:      intFrom(input, "sort", 0),
	}
	if rec.Name == "" {
		rec.Name = "目录"
	}
	if rec.ID == "" {
		if err := srv.db.CreateFolder(rec); err != nil {
			respondError(id, -32603, err.Error())
			return
		}
	} else {
		// 防环
		if rec.ParentID != "" {
			if rec.ParentID == rec.ID {
				respondError(id, -32603, "目录不能移动到自身下")
				return
			}
			if anc, err := srv.db.IsFolderAncestorOf(rec.ID, rec.ParentID); err == nil && anc {
				respondError(id, -32603, "目录不能移动到其子孙目录下")
				return
			}
		}
		if err := srv.db.UpdateFolder(rec); err != nil {
			respondError(id, -32603, err.Error())
			return
		}
	}
	respond(id, rec)
}

func cmdFolderDelete(id int64, input map[string]interface{}) {
	if err := srv.db.DeleteFolder(strFrom(input, "id")); err != nil {
		respondError(id, -32603, err.Error())
		return
	}
	respond(id, map[string]interface{}{"ok": true})
}

func cmdFolderReorder(id int64, input map[string]interface{}) {
	projectID := strFrom(input, "projectId")
	parentID := strFrom(input, "parentId")
	ids := strSliceFrom(input, "ids")
	if len(ids) == 0 {
		respondError(id, -32602, "缺少 ids")
		return
	}
	if parentID != "" {
		for _, fid := range ids {
			if anc, err := srv.db.IsFolderAncestorOf(fid, parentID); err == nil && anc {
				respondError(id, -32603, "目录不能移动到其子孙目录下")
				return
			}
		}
	}
	if err := srv.db.ReorderFolders(projectID, parentID, ids); err != nil {
		respondError(id, -32603, err.Error())
		return
	}
	for _, fid := range ids {
		f, err := srv.db.GetFolder(fid)
		if err != nil {
			respondError(id, -32603, err.Error())
			return
		}
		if f != nil && f.ProjectID != projectID {
			if err := srv.db.UpdateFolderSubtreeProject(fid, projectID); err != nil {
				respondError(id, -32603, err.Error())
				return
			}
		}
	}
	respond(id, map[string]interface{}{"ok": true})
}

// ---------- 文档 ----------

func cmdDocList(id int64, input map[string]interface{}) {
	list, err := srv.db.ListDocs(strFrom(input, "projectId"))
	if err != nil {
		respondError(id, -32603, err.Error())
		return
	}
	respond(id, list)
}

func cmdDocSave(id int64, input map[string]interface{}) {
	rec := &HttpDoc{
		ID:        strFrom(input, "id"),
		ProjectID: strFrom(input, "projectId"),
		FolderID:  strFrom(input, "folderId"),
		Name:      strings.TrimSpace(strFrom(input, "name")),
		Content:   strFrom(input, "content"),
		Sort:      intFrom(input, "sort", 0),
	}
	if rec.Name == "" {
		rec.Name = "未命名文档"
	}
	if rec.ID == "" {
		if err := srv.db.CreateDoc(rec); err != nil {
			respondError(id, -32603, err.Error())
			return
		}
	} else {
		cur, err := srv.db.GetDoc(rec.ID)
		if err != nil {
			respondError(id, -32603, err.Error())
			return
		}
		if cur == nil {
			respondError(id, -32603, "文档不存在")
			return
		}
		cur.Name = rec.Name
		cur.Content = rec.Content
		cur.Sort = rec.Sort
		if err := srv.db.UpdateDoc(cur); err != nil {
			respondError(id, -32603, err.Error())
			return
		}
		rec = cur
	}
	respond(id, rec)
}

func cmdDocGet(id int64, input map[string]interface{}) {
	rec, err := srv.db.GetDoc(strFrom(input, "id"))
	if err != nil {
		respondError(id, -32603, err.Error())
		return
	}
	if rec == nil {
		respondError(id, -32603, "文档不存在")
		return
	}
	respond(id, rec)
}

func cmdDocDelete(id int64, input map[string]interface{}) {
	if err := srv.db.DeleteDoc(strFrom(input, "id")); err != nil {
		respondError(id, -32603, err.Error())
		return
	}
	respond(id, map[string]interface{}{"ok": true})
}

// ---------- 环境 ----------

func cmdEnvList(id int64, input map[string]interface{}) {
	list, err := srv.db.ListEnvironments(strFrom(input, "projectId"))
	if err != nil {
		respondError(id, -32603, err.Error())
		return
	}
	respond(id, list)
}

func cmdEnvSave(id int64, input map[string]interface{}) {
	rec := &HttpEnvironment{
		ID:        strFrom(input, "id"),
		ProjectID: strFrom(input, "projectId"),
		Name:      strings.TrimSpace(strFrom(input, "name")),
		Variables: strFrom(input, "variables"),
		Sort:      intFrom(input, "sort", 0),
	}
	if rec.Name == "" {
		rec.Name = "环境"
	}
	if rec.Variables == "" {
		rec.Variables = "[]"
	}
	if rec.ID == "" {
		if err := srv.db.CreateEnvironment(rec); err != nil {
			respondError(id, -32603, err.Error())
			return
		}
	} else {
		if err := srv.db.UpdateEnvironment(rec); err != nil {
			respondError(id, -32603, err.Error())
			return
		}
	}
	respond(id, rec)
}

func cmdEnvDelete(id int64, input map[string]interface{}) {
	if err := srv.db.DeleteEnvironment(strFrom(input, "id")); err != nil {
		respondError(id, -32603, err.Error())
		return
	}
	respond(id, map[string]interface{}{"ok": true})
}

func cmdEnvResolve(id int64, input map[string]interface{}) {
	vars := srv.collectEnvVars(strFrom(input, "environmentId"))
	out := map[string]interface{}{
		"url":       substituteVars(strFrom(input, "url"), vars),
		"body":      substituteVars(strFrom(input, "body"), vars),
		"authToken": substituteVars(strFrom(input, "authToken"), vars),
		"authUser":  substituteVars(strFrom(input, "authUser"), vars),
		"authPass":  substituteVars(strFrom(input, "authPass"), vars),
	}
	respond(id, out)
}

// ---------- 导入 ----------

func cmdImportPostman(id int64, input map[string]interface{}) {
	jsonStr := strFrom(input, "json")
	if jsonStr == "" {
		// 兼容 content 字段
		jsonStr = strFrom(input, "content")
	}
	if jsonStr == "" {
		respondError(id, -32602, "缺少 json 内容")
		return
	}
	res, err := srv.ImportPostman(jsonStr)
	if err != nil {
		respondError(id, -32603, err.Error())
		return
	}
	respond(id, res)
}
