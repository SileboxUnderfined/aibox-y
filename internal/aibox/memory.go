package aibox

import (
	"errors"
	"log/slog"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"aiboxy/internal/chats"
	"aiboxy/internal/memory"
)

// rememberRule asks the model to mark facts worth keeping; the marks are cut
// from the answer before it is spoken.
const rememberRule = "Если пользователь сообщил о себе что-то устойчивое, что пригодится в будущих разговорах (имя, близкие, питомцы, город, работа, увлечения, как ему удобнее получать ответы), коротко скажи в ответе, что запомнила, и последней строкой добавь служебную пометку [запомнить: факт одной короткой фразой о пользователе в третьем лице]. Пометку не озвучивают. Не запоминай пароли, коды, номера карт и документов, точный адрес и то, что уже есть в списке. Если ничего нового нет, пометку не добавляй."

var rememberMark = regexp.MustCompile(`(?i)\[\[?\s*запомн[а-яё]*\s*:\s*([^\]\n]*?)\s*\]?\]`)

// memoryOn reports whether facts are shared between chats at all.
func (r *Resolver) memoryOn() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.memoryOnLocked()
}

func (r *Resolver) memoryOnLocked() bool {
	return r.opt.Memory != nil && !r.state.MemoryOff
}

// memoryPrompt is what a chat with this model gets on top of the system
// prompt; auto says the model was asked to mark facts itself.
func (r *Resolver) memoryPrompt(modelKey string, temporary bool) (block string, auto bool) {
	r.mu.Lock()
	on := r.memoryOnLocked() && !slices.Contains(r.state.MemoryExcluded, modelKey)
	auto = on && !r.state.MemoryAutoOff && !temporary
	r.mu.Unlock()
	if !on {
		return "", false
	}
	block = r.opt.Memory.Prompt()
	if auto {
		if block != "" {
			block += "\n\n"
		}
		block += rememberRule
	}
	return block, auto
}

// takeFacts cuts the model's [запомнить: …] marks out of an answer and,
// when allowed, saves what they say.
func (r *Resolver) takeFacts(answer string, save bool) string {
	marks := rememberMark.FindAllStringSubmatch(answer, 3)
	if len(marks) == 0 {
		return answer
	}
	answer = strings.TrimSpace(rememberMark.ReplaceAllString(answer, ""))
	if answer == "" {
		answer = "Запомнила."
	}
	if !save {
		return answer
	}
	for _, m := range marks {
		if _, err := r.opt.Memory.Add(m[1], "model"); err != nil && !errors.Is(err, memory.ErrEmpty) {
			slog.Warn("memory_add_failed", "full", errors.Is(err, memory.ErrFull))
		} else if err == nil {
			slog.Info("memory_fact_added", "source", "model")
		}
	}
	return answer
}

func (r *Resolver) handleMemory(c command, text string) string {
	switch c.kind {
	case cmdMemoryOn, cmdMemoryOff:
		off := c.kind == cmdMemoryOff
		if err := r.update(func(s *state) { s.MemoryOff = off }); err != nil {
			slog.Warn("state_save_failed")
		}
		if off {
			return "Выключила общую память. Факты сохранены, но в чаты больше не попадают."
		}
		return "Включила общую память."
	case cmdAutoMemoryOn, cmdAutoMemoryOff:
		off := c.kind == cmdAutoMemoryOff
		if err := r.update(func(s *state) { s.MemoryAutoOff = off }); err != nil {
			slog.Warn("state_save_failed")
		}
		if off {
			return "Больше не запоминаю сама. Запомнить можно командой «запомни, что …»."
		}
		return "Снова запоминаю важное из разговоров."
	case cmdRemember:
		f, err := r.opt.Memory.Add(spokenTail(text, rememberPattern), "voice")
		switch {
		case errors.Is(err, memory.ErrFull):
			return "Память заполнена: в ней уже " + strconv.Itoa(memory.MaxFacts) + " фактов. Скажите «забудь, что …» или удалите лишнее в веб-панели."
		case err != nil:
			return "Не получилось запомнить."
		}
		slog.Info("memory_fact_added", "source", "voice")
		return "Запомнила: " + f.Text + "."
	case cmdForgetFact:
		f, err := r.opt.Memory.Forget(c.name)
		switch {
		case errors.Is(err, memory.ErrNotFound):
			return "Не нашла такого в памяти."
		case err != nil:
			return "Не получилось забыть."
		}
		return "Забыла: " + f.Text + "."
	case cmdForgetAll:
		if err := r.opt.Memory.Clear(); err != nil {
			return "Не получилось очистить память."
		}
		return "Забыла всё, что знала о вас."
	case cmdShowMemory:
		facts := r.opt.Memory.List()
		if len(facts) == 0 {
			return "Пока ничего о вас не помню."
		}
		shown := make([]string, 0, 10)
		for i, f := range facts {
			if i == 10 {
				break
			}
			shown = append(shown, f.Text)
		}
		out := "Я помню: " + strings.Join(shown, "; ") + "."
		if len(facts) > len(shown) {
			out += " И ещё " + strconv.Itoa(len(facts)-len(shown)) + ", весь список в веб-панели."
		}
		return out
	}
	return ""
}

// spokenTail returns the part of recognized text that the pattern's last
// group caught, as it was said.
func spokenTail(text string, re *regexp.Regexp) string {
	cleaned := cleanText(text)
	m := re.FindStringSubmatchIndex(cleaned)
	if m == nil || m[len(m)-2] < 0 {
		return ""
	}
	skip := len(strings.Fields(chats.Normalize(text))) - len(strings.Fields(cleaned)) + len(strings.Fields(cleaned[:m[len(m)-2]]))
	return dropWords(text, skip)
}
