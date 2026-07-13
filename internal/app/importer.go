package app

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxImportPackage  = 50 << 20
	maxImportExpanded = 100 << 20
	maxImportVersions = 5000
	maxImportFile     = 1 << 20
)

func (s *Server) importPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 POST")
		return
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		writeError(w, http.StatusUnsupportedMediaType, "content_type_required", "必须使用 multipart/form-data")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxImportPackage+(1<<20))
	if err := r.ParseMultipartForm(maxImportPackage); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_import", "上传包无效或超过 50 MiB")
		return
	}
	repositoryID := r.FormValue("repository_id")
	repo, ok := s.store.repository(repositoryID)
	if !ok || !repo.Enabled {
		writeError(w, http.StatusBadRequest, "repository_required", "必须选择一个已启用仓库")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "file_required", "缺少导入文件")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxImportPackage+1))
	if err != nil || len(data) > maxImportPackage {
		writeError(w, http.StatusBadRequest, "package_too_large", "上传包超过 50 MiB")
		return
	}
	var versions []ImportVersion
	switch strings.ToLower(filepath.Ext(header.Filename)) {
	case ".csv":
		versions, err = parseImportCSV(bytes.NewReader(data))
	case ".zip":
		versions, err = parseImportZIP(data)
	default:
		err = errors.New("仅支持 .csv 或 .zip")
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_import", err.Error())
		return
	}
	if err := s.git.TestAndClone(r.Context(), repo); err != nil {
		writeError(w, http.StatusBadGateway, "repository_test_failed", err.Error())
		return
	}
	head, err := s.git.Head(r.Context(), repo)
	if err != nil {
		writeError(w, http.StatusBadGateway, "git_error", err.Error())
		return
	}
	id, _ := randomToken(20)
	sum := sha256.Sum256(data)
	preview := &ImportPreview{ID: id, AdminSession: sessionIDFrom(r.Context()), RepositoryID: repo.ID, PackageHash: hex.EncodeToString(sum[:]), BaseHead: head, Versions: versions, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(30 * time.Minute)}
	s.mu.Lock()
	activeForSession, totalBytes := 0, 0
	for _, current := range s.previews {
		if time.Now().After(current.ExpiresAt) {
			delete(s.previews, current.ID)
			continue
		}
		if current.AdminSession == preview.AdminSession {
			activeForSession++
		}
		for _, version := range current.Versions {
			totalBytes += len(version.Content)
		}
	}
	for _, version := range versions {
		totalBytes += len(version.Content)
	}
	if activeForSession >= 2 || totalBytes > 128<<20 {
		s.mu.Unlock()
		writeError(w, http.StatusTooManyRequests, "preview_limit", "预览数量或内存上限已达到，请执行或等待现有预览过期")
		return
	}
	s.previews[id] = preview
	s.mu.Unlock()
	time.AfterFunc(30*time.Minute, func() {
		s.mu.Lock()
		if current, ok := s.previews[id]; ok && current == preview {
			delete(s.previews, id)
		}
		s.mu.Unlock()
	})
	writeJSON(w, http.StatusCreated, preview)
}

func (s *Server) importRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 POST")
		return
	}
	var input struct {
		PreviewID   string `json:"preview_id"`
		PackageHash string `json:"package_sha256"`
		Confirm     bool   `json:"confirm"`
	}
	if err := decodeJSON(w, r, &input, 16<<10); err != nil {
		return
	}
	if !input.Confirm {
		writeError(w, http.StatusBadRequest, "confirmation_required", "必须确认导入预览")
		return
	}
	s.mu.Lock()
	preview, ok := s.previews[input.PreviewID]
	if ok && (preview.AdminSession != sessionIDFrom(r.Context()) || time.Now().After(preview.ExpiresAt) || input.PackageHash != preview.PackageHash) {
		delete(s.previews, input.PreviewID)
		ok = false
	}
	s.mu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, "preview_expired", "预览不存在、已过期或哈希不匹配")
		return
	}
	repo, ok := s.store.repository(preview.RepositoryID)
	if !ok || !repo.Enabled {
		writeError(w, http.StatusBadRequest, "repository_unavailable", "目标仓库不可用")
		return
	}
	if err := s.git.TestAndClone(r.Context(), repo); err != nil {
		writeError(w, http.StatusBadGateway, "repository_test_failed", err.Error())
		return
	}
	head, err := s.git.Head(r.Context(), repo)
	if err != nil || head != preview.BaseHead {
		writeError(w, http.StatusConflict, "head_changed", "仓库 HEAD 已变化，请重新预览")
		return
	}
	for _, version := range preview.Versions {
		id, _ := randomToken(18)
		event := ArchiveEvent{ID: id, RepositoryID: repo.ID, LogicalPath: version.LogicalPath, Title: version.Title, ContentSHA256: version.SHA256, VersionAt: version.VersionAt, Source: "import:" + preview.PackageHash, Status: "queued", Content: version.Content}
		if err := s.store.saveEvent(event); err != nil {
			writeError(w, http.StatusInternalServerError, "store_error", err.Error())
			return
		}
	}
	s.mu.Lock()
	delete(s.previews, input.PreviewID)
	s.mu.Unlock()
	s.store.log("info", "import_queued", repo.ID, fmt.Sprintf("已入队 %d 个真实版本", len(preview.Versions)))
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "queued", "repository_id": repo.ID, "versions": len(preview.Versions)})
}

func parseImportCSV(reader io.Reader) ([]ImportVersion, error) {
	csvReader := csv.NewReader(io.LimitReader(reader, maxImportPackage+1))
	csvReader.FieldsPerRecord = -1
	csvReader.ReuseRecord = false
	records, err := csvReader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("CSV 解析失败: %w", err)
	}
	if len(records) < 2 {
		return nil, errors.New("CSV 至少需要表头和一条版本记录")
	}
	index := headerIndex(records[0])
	for _, required := range []string{"logical_path", "version_at", "content"} {
		if _, ok := index[required]; !ok {
			return nil, fmt.Errorf("CSV 缺少字段 %s", required)
		}
	}
	var versions []ImportVersion
	for line, record := range records[1:] {
		version, err := versionFromRecord(record, index, line+2, nil)
		if err != nil {
			return nil, err
		}
		versions = append(versions, version)
	}
	return validateVersions(versions)
}

func parseImportZIP(data []byte) ([]ImportVersion, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("ZIP 解析失败: %w", err)
	}
	files := make(map[string]*zip.File, len(reader.File))
	var expanded uint64
	for _, file := range reader.File {
		name, err := safeLogicalPath(file.Name)
		if err != nil || file.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("ZIP 包含不安全路径或符号链接: %s", file.Name)
		}
		if _, duplicate := files[filepath.ToSlash(name)]; duplicate {
			return nil, fmt.Errorf("ZIP 包含重复路径: %s", file.Name)
		}
		if file.UncompressedSize64 > maxImportExpanded || expanded > maxImportExpanded-file.UncompressedSize64 {
			return nil, errors.New("ZIP 解压后超过 100 MiB")
		}
		expanded += file.UncompressedSize64
		files[filepath.ToSlash(name)] = file
	}
	manifest, ok := files["manifest.csv"]
	if !ok {
		return nil, errors.New("ZIP 根目录缺少 manifest.csv")
	}
	manifestReader, err := manifest.Open()
	if err != nil {
		return nil, err
	}
	records, err := csv.NewReader(io.LimitReader(manifestReader, 4<<20)).ReadAll()
	manifestReader.Close()
	if err != nil || len(records) < 2 {
		return nil, errors.New("manifest.csv 无效或无版本记录")
	}
	index := headerIndex(records[0])
	for _, required := range []string{"logical_path", "version_at", "content_file", "sha256"} {
		if _, ok := index[required]; !ok {
			return nil, fmt.Errorf("manifest.csv 缺少字段 %s", required)
		}
	}
	var versions []ImportVersion
	for line, record := range records[1:] {
		contentFile := field(record, index, "content_file")
		clean, err := safeLogicalPath(contentFile)
		if err != nil {
			return nil, fmt.Errorf("第 %d 行 content_file 无效", line+2)
		}
		entry, ok := files[filepath.ToSlash(clean)]
		if !ok || entry.FileInfo().IsDir() {
			return nil, fmt.Errorf("第 %d 行内容文件不存在", line+2)
		}
		if entry.UncompressedSize64 > maxImportFile {
			return nil, fmt.Errorf("第 %d 行内容超过 1 MiB", line+2)
		}
		r, err := entry.Open()
		if err != nil {
			return nil, err
		}
		content, err := io.ReadAll(io.LimitReader(r, maxImportFile+1))
		r.Close()
		if err != nil || len(content) > maxImportFile {
			return nil, fmt.Errorf("第 %d 行内容读取失败", line+2)
		}
		version, err := versionFromRecord(record, index, line+2, content)
		if err != nil {
			return nil, err
		}
		if expected := strings.ToLower(field(record, index, "sha256")); expected != version.SHA256 {
			return nil, fmt.Errorf("第 %d 行 SHA-256 不匹配", line+2)
		}
		versions = append(versions, version)
	}
	return validateVersions(versions)
}

func versionFromRecord(record []string, index map[string]int, line int, contentBytes []byte) (ImportVersion, error) {
	logicalPath := field(record, index, "logical_path")
	if _, err := safeLogicalPath(logicalPath); err != nil {
		return ImportVersion{}, fmt.Errorf("第 %d 行 logical_path 无效", line)
	}
	versionAt, err := time.Parse(time.RFC3339, field(record, index, "version_at"))
	if err != nil || versionAt.After(time.Now().Add(5*time.Minute)) {
		return ImportVersion{}, fmt.Errorf("第 %d 行 version_at 必须为不晚于当前时间的 RFC 3339", line)
	}
	content := rawField(record, index, "content")
	if contentBytes != nil {
		content = string(contentBytes)
	}
	if len(content) == 0 || len(content) > maxImportFile || !utf8.ValidString(content) || strings.IndexByte(content, 0) >= 0 {
		return ImportVersion{}, fmt.Errorf("第 %d 行必须是 1 MiB 内的 UTF-8 纯文本", line)
	}
	title := field(record, index, "title")
	return ImportVersion{LogicalPath: logicalPath, Title: title, Content: content, SHA256: contentHash(content), VersionAt: versionAt, Line: line}, nil
}

func validateVersions(versions []ImportVersion) ([]ImportVersion, error) {
	if len(versions) == 0 || len(versions) > maxImportVersions {
		return nil, fmt.Errorf("版本数量必须为 1-%d", maxImportVersions)
	}
	seen := map[string]bool{}
	for _, version := range versions {
		key := strings.ToLower(version.LogicalPath) + "\x00" + version.VersionAt.Format(time.RFC3339Nano)
		if seen[key] {
			return nil, fmt.Errorf("重复版本: %s @ %s", version.LogicalPath, version.VersionAt.Format(time.RFC3339))
		}
		seen[key] = true
	}
	sort.SliceStable(versions, func(i, j int) bool {
		if versions[i].VersionAt.Equal(versions[j].VersionAt) {
			return versions[i].Line < versions[j].Line
		}
		return versions[i].VersionAt.Before(versions[j].VersionAt)
	})
	return versions, nil
}

func headerIndex(header []string) map[string]int {
	result := map[string]int{}
	for i, name := range header {
		result[strings.ToLower(strings.TrimSpace(strings.TrimPrefix(name, "\ufeff")))] = i
	}
	return result
}

func field(record []string, index map[string]int, name string) string {
	return strings.TrimSpace(rawField(record, index, name))
}

func rawField(record []string, index map[string]int, name string) string {
	i, ok := index[name]
	if !ok || i >= len(record) {
		return ""
	}
	return record[i]
}

var _ multipart.File
