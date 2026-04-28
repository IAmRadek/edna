package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const (
	ednaFileExt       = ".edna.txt"
	ednaEncryptedExt  = ".encr.edna.txt"
	notesMetadataName = "__metadata.edna.json"
	maxNoteBodySize   = 16 << 20
	maxJSONBodySize   = 1 << 20
)

var invalidNoteFileNames = regexp.MustCompile(`(?i)^(con|prn|aux|nul|com\d|lpt|\.|\.\.|\d)$`)

type noteListResponse struct {
	All       []string `json:"all"`
	Encrypted []string `json:"encrypted"`
}

type noteBodyResponse struct {
	Content string `json:"content"`
	Rev     string `json:"rev"`
}

type revResponse struct {
	Rev string `json:"rev"`
}

type noteCreateRequest struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

type noteRenameRequest struct {
	OldName string `json:"oldName"`
	NewName string `json:"newName"`
	Content string `json:"content"`
	Rev     string `json:"rev"`
}

func getNotesDirMust() string {
	res := filepath.Join(getDataDirMust(), "notes")
	err := os.MkdirAll(res, 0755)
	must(err)
	return res
}

func noteNameToFileName(name string, encrypted bool) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", e("missing note name")
	}
	ext := ednaFileExt
	if encrypted {
		ext = ednaEncryptedExt
	}
	return toFileName(name + ext), nil
}

func noteFilePath(name string, encrypted bool) (string, error) {
	fileName, err := noteNameToFileName(name, encrypted)
	if err != nil {
		return "", err
	}
	dir := getNotesDirMust()
	p := filepath.Join(dir, fileName)
	rel, err := filepath.Rel(dir, p)
	if err != nil {
		return "", err
	}
	if rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", e("invalid note path")
	}
	return p, nil
}

func existingNoteFilePath(name string) (string, bool, error) {
	plainPath, err := noteFilePath(name, false)
	if err != nil {
		return "", false, err
	}
	if _, err := os.Stat(plainPath); err == nil {
		return plainPath, false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}
	encrPath, err := noteFilePath(name, true)
	if err != nil {
		return "", false, err
	}
	if _, err := os.Stat(encrPath); err == nil {
		return encrPath, true, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}
	return plainPath, false, os.ErrNotExist
}

func noteRev(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func readFileWithRev(path string) ([]byte, string, error) {
	d, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	return d, noteRev(d), nil
}

func atomicWriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-edna-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func readLimitedBody(w http.ResponseWriter, r *http.Request, maxBytes int64) ([]byte, error) {
	defer r.Body.Close()
	return io.ReadAll(http.MaxBytesReader(w, r.Body, maxBytes))
}

func sendJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func sendJSONError(w http.ResponseWriter, status int, msg string) {
	sendJSON(w, status, map[string]string{"error": msg})
}

func noteNameFromFileName(fileName string) (string, bool, bool) {
	if !isValidFileName(fileName) {
		return "", false, false
	}
	isEncrypted := strings.HasSuffix(fileName, ednaEncryptedExt)
	isPlain := strings.HasSuffix(fileName, ednaFileExt)
	if !isEncrypted && !isPlain {
		return "", false, false
	}
	encodedName := strings.TrimSuffix(fileName, ednaEncryptedExt)
	encodedName = strings.TrimSuffix(encodedName, ednaFileExt)
	name := fromFileName(encodedName)
	if name == "" {
		return "", false, false
	}
	return name, isEncrypted, true
}

func listServerNotes() (noteListResponse, error) {
	entries, err := os.ReadDir(getNotesDirMust())
	if err != nil {
		return noteListResponse{}, err
	}
	res := noteListResponse{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name, encrypted, ok := noteNameFromFileName(entry.Name())
		if !ok {
			continue
		}
		res.All = append(res.All, name)
		if encrypted {
			res.Encrypted = append(res.Encrypted, name)
		}
	}
	return res, nil
}

func handleNotesList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		sendJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	res, err := listServerNotes()
	if err != nil {
		sendJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	sendJSON(w, http.StatusOK, res)
}

func handleNote(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		sendJSONError(w, http.StatusBadRequest, "missing note name")
		return
	}

	switch r.Method {
	case http.MethodGet:
		path, _, err := existingNoteFilePath(name)
		if errors.Is(err, os.ErrNotExist) {
			sendJSONError(w, http.StatusNotFound, "note not found")
			return
		}
		if err != nil {
			sendJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		data, rev, err := readFileWithRev(path)
		if err != nil {
			sendJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		sendJSON(w, http.StatusOK, noteBodyResponse{Content: string(data), Rev: rev})
	case http.MethodPut:
		data, err := readLimitedBody(w, r, maxNoteBodySize)
		if err != nil {
			sendJSONError(w, http.StatusRequestEntityTooLarge, err.Error())
			return
		}
		path, encrypted, err := existingNoteFilePath(name)
		if err == nil {
			if rev := r.URL.Query().Get("rev"); rev != "" {
				_, oldRev, err := readFileWithRev(path)
				if err != nil {
					sendJSONError(w, http.StatusInternalServerError, err.Error())
					return
				}
				if oldRev != rev {
					sendJSONError(w, http.StatusConflict, "note changed on server")
					return
				}
			}
		} else if errors.Is(err, os.ErrNotExist) {
			path, err = noteFilePath(name, encrypted)
			if err != nil {
				sendJSONError(w, http.StatusBadRequest, err.Error())
				return
			}
		} else {
			sendJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := atomicWriteFile(path, data); err != nil {
			sendJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		sendJSON(w, http.StatusOK, revResponse{Rev: noteRev(data)})
	case http.MethodDelete:
		path, _, err := existingNoteFilePath(name)
		if errors.Is(err, os.ErrNotExist) {
			sendJSON(w, http.StatusOK, map[string]bool{"ok": true})
			return
		}
		if err != nil {
			sendJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := os.Remove(path); err != nil {
			sendJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		sendJSON(w, http.StatusOK, map[string]bool{"ok": true})
	default:
		sendJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func handleNoteCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sendJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	data, err := readLimitedBody(w, r, maxJSONBodySize)
	if err != nil {
		sendJSONError(w, http.StatusRequestEntityTooLarge, err.Error())
		return
	}
	var req noteCreateRequest
	if err := json.Unmarshal(data, &req); err != nil {
		sendJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	path, err := noteFilePath(req.Name, false)
	if err != nil {
		sendJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, _, err := existingNoteFilePath(req.Name); err == nil {
		sendJSONError(w, http.StatusConflict, "note already exists")
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		sendJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	content := []byte(req.Content)
	if err := atomicWriteFile(path, content); err != nil {
		sendJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	sendJSON(w, http.StatusOK, revResponse{Rev: noteRev(content)})
}

func handleNoteRename(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sendJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	data, err := readLimitedBody(w, r, maxJSONBodySize)
	if err != nil {
		sendJSONError(w, http.StatusRequestEntityTooLarge, err.Error())
		return
	}
	var req noteRenameRequest
	if err := json.Unmarshal(data, &req); err != nil {
		sendJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	oldPath, encrypted, err := existingNoteFilePath(req.OldName)
	if err != nil {
		sendJSONError(w, http.StatusNotFound, "old note not found")
		return
	}
	if req.Rev != "" {
		_, oldRev, err := readFileWithRev(oldPath)
		if err != nil {
			sendJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if oldRev != req.Rev {
			sendJSONError(w, http.StatusConflict, "note changed on server")
			return
		}
	}
	if _, _, err := existingNoteFilePath(req.NewName); err == nil {
		sendJSONError(w, http.StatusConflict, "new note already exists")
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		sendJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	newPath, err := noteFilePath(req.NewName, encrypted)
	if err != nil {
		sendJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	content := []byte(req.Content)
	if req.Content == "" {
		content, _, err = readFileWithRev(oldPath)
		if err != nil {
			sendJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if err := atomicWriteFile(newPath, content); err != nil {
		sendJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := os.Remove(oldPath); err != nil {
		sendJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	sendJSON(w, http.StatusOK, revResponse{Rev: noteRev(content)})
}

func handleNotesMetadata(w http.ResponseWriter, r *http.Request) {
	path := filepath.Join(getNotesDirMust(), notesMetadataName)
	switch r.Method {
	case http.MethodGet:
		data, rev, err := readFileWithRev(path)
		if errors.Is(err, os.ErrNotExist) {
			sendJSON(w, http.StatusOK, noteBodyResponse{Content: "[]", Rev: noteRev([]byte("[]"))})
			return
		}
		if err != nil {
			sendJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		sendJSON(w, http.StatusOK, noteBodyResponse{Content: string(data), Rev: rev})
	case http.MethodPut:
		data, err := readLimitedBody(w, r, maxJSONBodySize)
		if err != nil {
			sendJSONError(w, http.StatusRequestEntityTooLarge, err.Error())
			return
		}
		if rev := r.URL.Query().Get("rev"); rev != "" {
			_, oldRev, err := readFileWithRev(path)
			if err == nil && oldRev != rev {
				sendJSONError(w, http.StatusConflict, "metadata changed on server")
				return
			}
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				sendJSONError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		if err := atomicWriteFile(path, data); err != nil {
			sendJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		sendJSON(w, http.StatusOK, revResponse{Rev: noteRev(data)})
	default:
		sendJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func toFileName(s string) string {
	if invalidNoteFileNames.MatchString(s) {
		return stringHexEscape(s)
	}
	var b strings.Builder
	for _, r := range s {
		if charNeedsEscaping(r) {
			b.WriteString(stringHexEscape(string(r)))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func fromFileName(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '%' || i+4 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		var v uint64
		_, err := fmtSscanfHex4(s[i+1:i+5], &v)
		if err != nil {
			b.WriteByte(s[i])
			continue
		}
		b.WriteRune(rune(v))
		i += 4
	}
	return b.String()
}

func isValidFileName(s string) bool {
	return toFileName(fromFileName(s)) == s
}

func stringHexEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		b.WriteString("%")
		b.WriteString(strings.ToLower(hex.EncodeToString([]byte{byte(r >> 8), byte(r)})))
	}
	return b.String()
}

func charNeedsEscaping(r rune) bool {
	if r >= '0' && r <= '9' {
		return false
	}
	if r >= 'a' && r <= 'z' {
		return false
	}
	if r >= 'A' && r <= 'Z' {
		return false
	}
	return !strings.ContainsRune(" !#$()+,-.=@[]_()~", r)
}

func fmtSscanfHex4(s string, v *uint64) (int, error) {
	if len(s) != 4 {
		return 0, e("invalid hex")
	}
	n, err := strconv.ParseUint(s, 16, 16)
	if err != nil {
		return 0, err
	}
	*v = n
	return 1, nil
}
