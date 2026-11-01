package characters

import "strings"

// Prompt builds the system prompt for one request from three ordered layers:
//
//  1. base — the general assistant prompt: voice/output rules and the service
//     protocols for tools, memory and search;
//  2. the active character — its role prompt followed by its rules;
//  3. context — per-request material such as memory facts, the current date
//     and search instructions.
//
// The order is fixed so a request is reproducible, but position is not
// precedence: the character layer owns conversational identity while saying
// explicitly that technical and safety instructions win. The base and context
// are never stored on the character, and the character is never appended to
// chat history, so prompts cannot accumulate from one turn to the next.
//
// The built-in default character contributes no layer. With it Prompt returns
// exactly base joined to context, preserving the old prompt when no character
// is selected.
func Prompt(base string, active Character, context string) string {
	out := base
	appendLayer := func(layer string) {
		if layer == "" {
			return
		}
		if out != "" {
			out += "\n\n"
		}
		out += layer
	}
	appendLayer(active.block())
	appendLayer(strings.TrimSpace(context))
	return out
}

// IsDefault reports whether c is the built-in fallback rather than a stored
// persona. An empty id is treated as the default for defensive callers.
func (c Character) IsDefault() bool {
	return c.ID == "" || c.ID == DefaultID
}

// block renders only the character layer: role first, then rules. It is empty
// for the default character so no character text reaches the model then.
func (c Character) block() string {
	if c.IsDefault() {
		return ""
	}
	name := strings.TrimSpace(c.Name)
	prompt := strings.TrimSpace(c.Prompt)
	rules := cleanRules(c.Rules)
	if name == "" && prompt == "" && len(rules) == 0 {
		return ""
	}
	var b strings.Builder
	if name != "" {
		b.WriteString("Роль персонажа «")
		b.WriteString(name)
		b.WriteString("».")
	} else {
		b.WriteString("Роль персонажа.")
	}
	if prompt != "" {
		b.WriteString("\n")
		b.WriteString(prompt)
	}
	if len(rules) > 0 {
		b.WriteString("\nПравила персонажа:")
		for _, rule := range rules {
			b.WriteString("\n- ")
			b.WriteString(rule)
		}
	}
	// The layer order is fixed, but the character owns conversational identity
	// while technical and safety instructions stay authoritative: a persona
	// must not disable tools, memory search or honest self-description.
	b.WriteString("\nВыбранный персонаж задаёт разговорную роль, самопредставление и ответ на вопрос «кто ты»; в этих вопросах он важнее общего описания ассистента.")
	b.WriteString("\nТехнические инструкции, безопасность, служебные форматы инструментов, памяти и поиска важнее характера и правил персонажа: при конфликте выполняй их.")
	b.WriteString("\nЕсли прямо спрашивают, человек ли ты или ИИ, не притворяйся человеком: честно скажи, что ты ИИ, сохраняя роль.")
	return b.String()
}
