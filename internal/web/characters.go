package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"

	"aiboxy/internal/aibox"
	"aiboxy/internal/characters"
)

// characterID matches exactly the ids characters.NewID creates. "default" is
// allowed as well, so the API can answer 400 rather than 404 for the reserved
// fallback.
var characterID = regexp.MustCompile(`^[0-9a-f]{16}$`)

func (s *Server) listCharacters(w http.ResponseWriter, _ *http.Request) {
	info, err := s.Backend.Characters()
	if err != nil {
		characterFailure(w, err, "не удалось прочитать персонажей")
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) createCharacter(w http.ResponseWriter, r *http.Request) {
	var in aibox.CharacterInput
	if err := decodeCharacterBody(w, r, &in); err != nil {
		characterBadRequest(w, err)
		return
	}
	if _, err := s.Backend.CreateCharacter(in); err != nil {
		characterFailure(w, err, "не удалось создать персонажа")
		return
	}
	s.writeCharacters(w, http.StatusCreated)
}

func (s *Server) updateCharacter(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id != characters.DefaultID && !characterID.MatchString(id) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "персонаж не найден"})
		return
	}
	var in aibox.CharacterInput
	if err := decodeCharacterBody(w, r, &in); err != nil {
		characterBadRequest(w, err)
		return
	}
	if _, err := s.Backend.UpdateCharacter(id, in); err != nil {
		characterFailure(w, err, "не удалось сохранить персонажа")
		return
	}
	s.writeCharacters(w, http.StatusOK)
}

func (s *Server) deleteCharacter(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id != characters.DefaultID && !characterID.MatchString(id) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "персонаж не найден"})
		return
	}
	if err := s.Backend.DeleteCharacter(id); err != nil {
		characterFailure(w, err, "не удалось удалить персонажа")
		return
	}
	s.writeCharacters(w, http.StatusOK)
}

func (s *Server) activateCharacter(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := decodeCharacterBody(w, r, &req); err != nil {
		characterBadRequest(w, err)
		return
	}
	if req.ID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "не указан персонаж"})
		return
	}
	if err := s.Backend.SetActiveCharacter(req.ID); err != nil {
		characterFailure(w, err, "не удалось переключить персонажа")
		return
	}
	s.writeCharacters(w, http.StatusOK)
}

// resetCharacter selects the built-in fallback for every chat. The default id
// is never stored as a normal character, and the existing prompts are not
// touched.
func (s *Server) resetCharacter(w http.ResponseWriter, _ *http.Request) {
	if err := s.Backend.SetActiveCharacter(characters.DefaultID); err != nil {
		characterFailure(w, err, "не удалось сбросить персонажа")
		return
	}
	s.writeCharacters(w, http.StatusOK)
}

func (s *Server) writeCharacters(w http.ResponseWriter, code int) {
	info, err := s.Backend.Characters()
	if err != nil {
		characterFailure(w, err, "не удалось прочитать персонажей")
		return
	}
	writeJSON(w, code, info)
}

// decodeCharacterBody limits the request size, rejects unknown fields and
// rejects a second JSON value. Validation of names, prompts and rules happens
// in the store, so direct API calls get the same bounds as the panel.
func decodeCharacterBody(w http.ResponseWriter, r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("unexpected trailing data")
		}
		return err
	}
	return nil
}

func characterBadRequest(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "запрос слишком большой"})
		return
	}
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": "некорректный запрос"})
}

// characterFailure maps store errors to short messages. Unknown failures are
// logged locally and answered with a generic message, so a file path or other
// implementation detail never reaches the browser.
func characterFailure(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, aibox.ErrCharactersUnavailable):
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "персонажи не настроены"})
	case errors.Is(err, characters.ErrEmptyName):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "укажите имя персонажа"})
	case errors.Is(err, characters.ErrNameTooLong):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("имя не длиннее %d символов", characters.MaxNameRunes)})
	case errors.Is(err, characters.ErrPromptTooLong):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("промпт не длиннее %d символов", characters.MaxPromptRunes)})
	case errors.Is(err, characters.ErrTooManyRules):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("не больше %d правил", characters.MaxRules)})
	case errors.Is(err, characters.ErrRuleTooLong):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("правило не длиннее %d символов", characters.MaxRuleRunes)})
	case errors.Is(err, characters.ErrDuplicateName):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "персонаж с таким именем уже есть"})
	case errors.Is(err, characters.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "персонаж не найден"})
	case errors.Is(err, characters.ErrDefault):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "персонажа по умолчанию нельзя изменить или удалить"})
	default:
		slog.Error("character_api_failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fallback})
	}
}
