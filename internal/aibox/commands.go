package aibox

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"aiboxy/internal/chats"
)

type kind int

const (
	cmdNone kind = iota
	cmdNew
	cmdContinue
	cmdTemporary
	cmdForget
	cmdList
	cmdExit
	cmdModel       // explicit "модель X": answers even when X is unknown
	cmdModelSwitch // "переключись на X": only when X names a model
	cmdWhichModel
	cmdSetPrompt
	cmdResetPrompt
	cmdShowPrompt
	cmdQuick // "спроси [модель|нейросеть] X": a new saved chat that starts with question X
	cmdSearchOn
	cmdSearchOff
	cmdRemember
	cmdForgetFact
	cmdForgetAll
	cmdShowMemory
	cmdMemoryOn
	cmdMemoryOff
	cmdAutoMemoryOn
	cmdAutoMemoryOff
	cmdStop // "стоп" over an answer; only in commandDefs, never parsed as a command
)

// isMemory marks commands that work only while the shared memory is on.
func (k kind) isMemory() bool {
	return k == cmdRemember || k == cmdForgetFact || k == cmdForgetAll || k == cmdShowMemory
}

// isMemorySwitch marks commands that turn the shared memory on and off.
func (k kind) isMemorySwitch() bool {
	return k == cmdMemoryOn || k == cmdMemoryOff || k == cmdAutoMemoryOn || k == cmdAutoMemoryOff
}

type command struct {
	kind kind
	name string
	re   *regexp.Regexp // what matched, for commands that need the spoken tail
}

type argMode int

const (
	argNone     argMode = iota // the whole phrase, nothing after it
	argOptional                // "новый чат [название]"
	argRequired                // "продолжи чат <название>"
)

// commandDef is one voice command. The order of commandDefs is the order
// built-in patterns are tried in; exit and stop are checked separately.
type commandDef struct {
	id      string
	kind    kind
	title   string // for the web panel
	args    argMode
	re      *regexp.Regexp
	example []string // what the panel shows as built-in phrases
}

var commandDefs = []commandDef{
	{"quick", cmdQuick, "Быстрый вопрос", argOptional, quickPattern, []string{"спроси", "спроси нейросеть", "задай вопрос"}},
	{"new_chat", cmdNew, "Новый чат", argOptional, regexp.MustCompile(`^(?:создай |создать |начни |начать |открой )?нов(?:ый|ой) чат(?: (.+))?$`), []string{"новый чат", "создай новый чат"}},
	{"continue_chat", cmdContinue, "Продолжить чат", argRequired, regexp.MustCompile(`^(?:продолжи(?:ть|м)?|продолжаем|открой|открыть|вернись в|вернуться в|верни) чат (.+)$`), []string{"продолжи чат", "открой чат", "вернись в чат"}},
	{"temporary_chat", cmdTemporary, "Временный чат", argNone, regexp.MustCompile(`^(?:давай )?поговори(?:ть|м)(?: со мной)?$`), []string{"поговорим", "давай поговорим", "поговори со мной"}},
	{"forget_chat", cmdForget, "Удалить чат", argRequired, regexp.MustCompile(`^(?:забудь|забыть|удали|удалить) чат (.+)$`), []string{"забудь чат", "удали чат"}},
	{"forget_all", cmdForgetAll, "Очистить память", argNone, regexp.MustCompile(`^(?:(?:забудь|забудьте|сотри|удали) (?:все|всю) (?:обо мне|про меня|что (?:ты )?(?:знаешь|помнишь|запомнила) обо мне)|(?:очисти|сотри|удали) (?:всю )?память)$`), []string{"забудь всё обо мне", "очисти память"}},
	{"show_memory", cmdShowMemory, "Что в памяти", argNone, regexp.MustCompile(`^(?:что ты (?:обо мне |про меня )?(?:знаешь|помнишь|запомнила)(?: обо мне| про меня)?|что (?:есть )?в памяти|покажи память)$`), []string{"что ты обо мне знаешь", "покажи память"}},
	{"auto_memory_off", cmdAutoMemoryOff, "Выключить автозапоминание", argNone, regexp.MustCompile(`^(?:выключи|отключи) (?:авто ?запоминание|запоминание)$`), []string{"выключи запоминание"}},
	{"auto_memory_on", cmdAutoMemoryOn, "Включить автозапоминание", argNone, regexp.MustCompile(`^(?:включи) (?:авто ?запоминание|запоминание)$`), []string{"включи запоминание"}},
	{"memory_off", cmdMemoryOff, "Выключить память", argNone, regexp.MustCompile(`^(?:выключи|отключи) (?:общую )?память$`), []string{"выключи память"}},
	{"memory_on", cmdMemoryOn, "Включить память", argNone, regexp.MustCompile(`^включи (?:общую )?память$`), []string{"включи память"}},
	{"remember", cmdRemember, "Запомнить факт", argRequired, rememberPattern, []string{"запомни", "запомни что"}},
	{"forget_fact", cmdForgetFact, "Забыть факт", argRequired, forgetFactPattern, []string{"забудь", "забудь что"}},
	{"list_chats", cmdList, "Список чатов", argNone, regexp.MustCompile(`^(?:список чатов|мои чаты|какие (?:у меня )?(?:есть )?чаты(?: есть)?)$`), []string{"список чатов", "мои чаты"}},
	{"which_model", cmdWhichModel, "Какая модель", argNone, regexp.MustCompile(`^(?:какая (?:сейчас )?модель(?: сейчас)?|список моделей|какие (?:есть )?модели(?: есть)?)$`), []string{"какая модель", "список моделей"}},
	{"model", cmdModel, "Выбрать модель", argRequired, regexp.MustCompile(`^(?:выбери|выбрать|смени|сменить|поставь|переключи|переключись на|используй)? ?модель (?:на )?(.+)$`), []string{"модель", "смени модель на"}},
	{"model_switch", cmdModelSwitch, "Переключиться на модель", argRequired, regexp.MustCompile(`^(?:переключись на|переключи на|переключиться на|используй|давай) (.+)$`), []string{"переключись на", "используй"}},
	{"reset_prompt", cmdResetPrompt, "Сбросить промпт", argNone, regexp.MustCompile(`^(?:сбрось|сбросить|удали|убери|очисти) (?:системный промпт|промпт|роль)$`), []string{"сбрось промпт", "убери роль"}},
	{"show_prompt", cmdShowPrompt, "Какой промпт", argNone, regexp.MustCompile(`^(?:какой (?:сейчас )?(?:системный )?промпт|какая (?:у тебя )?роль)$`), []string{"какой промпт", "какая у тебя роль"}},
	{"search_on", cmdSearchOn, "Включить интернет", argNone, regexp.MustCompile(`^(?:включи|включить|разреши|верни) (?:нейросети )?(?:интернет|поиск в интернете)(?: (?:для )?нейросети)?$`), []string{"включи интернет"}},
	{"search_off", cmdSearchOff, "Выключить интернет", argNone, regexp.MustCompile(`^(?:выключи|выключить|отключи|отключить|запрети|убери) (?:нейросети )?(?:интернет|поиск в интернете)(?: (?:для )?нейросети)?$`), []string{"выключи интернет"}},
	{"set_prompt", cmdSetPrompt, "Задать промпт", argRequired, regexp.MustCompile(`^(?:(?:установи |поставь |новый |смени )?системный промпт|(?:установи |новая |смени )роль|твоя роль) (?:на |это )?(.+)$`), []string{"системный промпт", "твоя роль"}},
	{"exit", cmdExit, "Выйти из чата", argNone, exitPhrases, []string{"хватит", "закрой чат", "выход", "пока"}},
	{"stop", cmdStop, "Перебить ответ", argNone, stopPhrases, []string{"стоп", "хватит", "замолчи", "подожди", "понятно"}},
}

// Phrases changes how one command is said: Add are extra phrases, Replace
// drops the built-in ones (Replace with no Add turns the command off). In
// the config a plain list means Add.
type Phrases struct {
	Add     []string `json:"add,omitempty"`
	Replace bool     `json:"replace,omitempty"`
}

func (p *Phrases) UnmarshalJSON(data []byte) error {
	var list []string
	if json.Unmarshal(data, &list) == nil {
		*p = Phrases{Add: list}
		return nil
	}
	type plain Phrases
	return json.Unmarshal(data, (*plain)(p))
}

// addList is Add without blanks and repeats; nil Phrases have none.
func (p *Phrases) addList() []string {
	if p == nil {
		return nil
	}
	var out []string
	for _, a := range p.Add {
		if a = strings.TrimSpace(a); a != "" && !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	return out
}

func commandByID(id string) (commandDef, bool) {
	for _, d := range commandDefs {
		if d.id == id {
			return d, true
		}
	}
	return commandDef{}, false
}

// ValidateCommands checks phrases from the config before AIBox starts.
func ValidateCommands(phrases map[string]Phrases) error {
	for id, p := range phrases {
		if _, ok := commandByID(id); !ok {
			return fmt.Errorf("unknown command %q", id)
		}
		for _, a := range p.Add {
			if chats.Normalize(a) == "" {
				return fmt.Errorf("command %q: empty phrase", id)
			}
		}
	}
	return nil
}

type matcher struct {
	kind kind
	re   *regexp.Regexp
}

// commandSet is what the resolver recognizes: the user's phrases first,
// then the built-in ones in their usual order.
type commandSet struct {
	patterns   []matcher
	exit, stop []*regexp.Regexp
}

func buildCommands(phrases map[string]Phrases) *commandSet {
	cs := &commandSet{}
	var custom, builtin []matcher
	for _, d := range commandDefs {
		p := phrases[d.id]
		var res []*regexp.Regexp
		if re := phraseRegexp(p.Add, d.args); re != nil {
			res = append(res, re)
		}
		if !p.Replace {
			res = append(res, d.re)
		}
		switch d.kind {
		case cmdExit:
			cs.exit = res
		case cmdStop:
			cs.stop = res
		default:
			for i, re := range res {
				if i == 0 && len(p.Add) > 0 {
					custom = append(custom, matcher{d.kind, re})
				} else {
					builtin = append(builtin, matcher{d.kind, re})
				}
			}
		}
	}
	cs.patterns = append(custom, builtin...)
	return cs
}

// phraseRegexp matches any of the phrases, as normalized speech, with what
// follows them in the last group.
func phraseRegexp(phrases []string, args argMode) *regexp.Regexp {
	var alts []string
	for _, p := range phrases {
		if p = chats.Normalize(p); p != "" {
			alts = append(alts, regexp.QuoteMeta(p))
		}
	}
	if len(alts) == 0 {
		return nil
	}
	tail := map[argMode]string{argNone: `$`, argOptional: `(?: (.+))?$`, argRequired: ` (.+)$`}[args]
	return regexp.MustCompile(`^(?:` + strings.Join(alts, "|") + `)` + tail)
}

var defaultCommands = buildCommands(nil)

// quickPattern starts a quick question, in normalized text. The neuro group
// marks "спроси нейросеть", which never names a model.
var quickPattern = regexp.MustCompile(`^(?:(?:спроси(?:те)?|задай(?:те)? вопрос)(?: у)?(?P<neuro> (?:нейросет[ьи]|нейронк[уеи]|ии))?|вопрос(?P<neuro2> (?:нейросет[ьи]|нейронк[уеи]|ии)))(?: (.+))?$`)

// rememberPattern and forgetFactPattern keep the fact in their last group.
var (
	rememberPattern   = regexp.MustCompile(`^(?:запомни|запомните)(?: пожалуйста)?(?: что| обо мне| про меня)? (.+)$`)
	forgetFactPattern = regexp.MustCompile(`^(?:забудь|забудьте)(?: пожалуйста)?(?: что| про| о| об)? (.+)$`)
)

// stopPhrases cut a spoken answer short; the chat stays open.
var stopPhrases = regexp.MustCompile(`^(?:стоп|хватит|замолчи|помолчи|тихо|тише|подожди|погоди|достаточно|остановись|перестань|не надо|ладно|понятно|ясно)$`)

var exitPhrases = regexp.MustCompile(`^(?:хватит|стоп|выход|выйти|выйди|пока|закрой чат|закрыть чат|выйди из чата|выйти из чата|заверши чат|завершить чат|конец чата)$`)

// cleanText normalizes recognized speech and drops the wake word, which
// the firmware sets: "Алиса" or "Яндекс".
func cleanText(text string) string {
	t := chats.Normalize(text)
	for _, wake := range []string{"алиса ", "алис ", "яндекс "} {
		t = strings.TrimPrefix(t, wake)
	}
	return t
}

// parse recognizes a chat command; exit phrases count only inside a chat.
func (cs *commandSet) parse(text string, inChat bool) command {
	t := cleanText(text)
	for _, p := range cs.patterns {
		if m := p.re.FindStringSubmatch(t); m != nil {
			c := command{kind: p.kind, re: p.re}
			if len(m) > 1 {
				c.name = strings.TrimSpace(m[len(m)-1])
			}
			return c
		}
	}
	if inChat && matchAny(cs.exit, t) {
		return command{kind: cmdExit}
	}
	return command{kind: cmdNone}
}

// stops reports whether text cuts a spoken answer short.
func (cs *commandSet) stops(text string) bool {
	return matchAny(cs.stop, cleanText(text))
}

func matchAny(res []*regexp.Regexp, t string) bool {
	for _, re := range res {
		if re.MatchString(t) {
			return true
		}
	}
	return false
}
