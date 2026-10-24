package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"math"
	"slices"
	"unicode/utf8"

	"github.com/VVivid/vvivid-workspace/WebhookControl/domain"
	"github.com/VVivid/vvivid-workspace/WebhookControl/ports"
	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/common/types/traits"
)

type fieldProgram struct {
	name    string
	program cel.Program
}

// Plan owns a copy of one validated source snapshot. CEL can access only event;
// it has no I/O, secrets, clock, random state, or comprehension macros. has()
// remains available for optional fields. Evaluation uses a fresh bounded budget.
type Plan struct {
	source       domain.Source
	rules        map[domain.RevisionRef]cel.Program
	templates    map[domain.RevisionRef][]fieldProgram
	transporters map[domain.RevisionRef]domain.Transporter
	routes       []domain.Route
}

func Compile(ctx context.Context, config domain.Configuration, policy ports.DestinationPolicy) (*Plan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	config, err := copyConfiguration(config)
	if err != nil {
		return nil, err
	}
	env, err := cel.NewEnv(
		cel.Variable("event", cel.MapType(cel.StringType, cel.DynType)),
		cel.ClearMacros(), cel.Macros(cel.HasMacro),
		cel.ParserRecursionLimit(64), cel.ParserExpressionSizeLimit(MaxExpressionBytes),
		cel.Function("mask", cel.Overload("mask_string", []*cel.Type{cel.StringType}, cel.StringType,
			cel.UnaryBinding(func(ref.Val) ref.Val { return types.String("***") }))),
	)
	if err != nil {
		return nil, ErrConfiguration
	}
	plan := &Plan{
		source:       config.Source,
		rules:        map[domain.RevisionRef]cel.Program{},
		templates:    map[domain.RevisionRef][]fieldProgram{},
		transporters: map[domain.RevisionRef]domain.Transporter{},
		routes:       config.Routes,
	}
	for _, rule := range config.Rules {
		key := domain.RevisionRef{ID: rule.ID, Revision: rule.Revision}
		if !validRef(key) || rule.SourceID != config.Source.ID || plan.rules[key] != nil {
			return nil, ErrConfiguration
		}
		program, err := compileExpression(ctx, env, rule.Expression, true)
		if err != nil {
			return nil, err
		}
		plan.rules[key] = program
	}
	for _, template := range config.Templates {
		key := domain.RevisionRef{ID: template.ID, Revision: template.Revision}
		if !validRef(key) || template.SourceID != config.Source.ID {
			return nil, ErrConfiguration
		}
		if _, exists := plan.templates[key]; exists {
			return nil, ErrConfiguration
		}
		fields := make([]fieldProgram, 0, len(template.Fields))
		for _, name := range slices.Sorted(maps.Keys(template.Fields)) {
			program, err := compileExpression(ctx, env, template.Fields[name], false)
			if err != nil {
				return nil, err
			}
			fields = append(fields, fieldProgram{name: name, program: program})
		}
		plan.templates[key] = fields
	}
	for _, transporter := range config.Transporters {
		key := domain.RevisionRef{ID: transporter.ID, Revision: transporter.Revision}
		if !validRef(key) || transporter.SourceID != config.Source.ID || transporter.Kind != domain.TransporterHTTP {
			return nil, ErrConfiguration
		}
		if _, exists := plan.transporters[key]; exists {
			return nil, ErrConfiguration
		}
		if err := checkPolicy(ctx, policy, config.Source.ID, transporter); err != nil {
			return nil, err
		}
		plan.transporters[key] = transporter
	}
	seenRoutes := map[domain.RevisionRef]bool{}
	for _, route := range config.Routes {
		key := domain.RevisionRef{ID: route.ID, Revision: route.Revision}
		if !validRef(key) || route.SourceID != config.Source.ID || seenRoutes[key] || !validRef(route.Template) {
			return nil, ErrConfiguration
		}
		seenRoutes[key] = true
		if _, exists := plan.templates[route.Template]; !exists {
			return nil, ErrConfiguration
		}
		seenRules, seenTransporters := map[domain.RevisionRef]bool{}, map[domain.RevisionRef]bool{}
		for _, rule := range route.Rules {
			if plan.rules[rule] == nil || seenRules[rule] {
				return nil, ErrConfiguration
			}
			seenRules[rule] = true
		}
		for _, transporter := range route.Transporters {
			if _, exists := plan.transporters[transporter]; !exists || seenTransporters[transporter] {
				return nil, ErrConfiguration
			}
			seenTransporters[transporter] = true
		}
	}
	return plan, nil
}

func compileExpression(ctx context.Context, env *cel.Env, expression string, requireBoolean bool) (cel.Program, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ast, issues := env.Compile(expression)
	if issues.Err() != nil || requireBoolean && ast.OutputType() != cel.BoolType && ast.OutputType() != cel.DynType {
		return nil, ErrConfiguration
	}
	program, err := env.Program(ast, cel.CostLimit(MaxExpressionCost), cel.InterruptCheckFrequency(1))
	if err != nil {
		return nil, ErrConfiguration
	}
	return program, nil
}

type evaluation struct {
	ctx       context.Context
	variables map[string]any
	cost      uint64
}

func (e *evaluation) run(program cel.Program) (ref.Val, error) {
	if err := e.ctx.Err(); err != nil {
		return nil, err
	}
	value, details, err := program.ContextEval(e.ctx, e.variables)
	if err != nil || types.IsError(value) || types.IsUnknown(value) {
		return nil, ErrEvaluation
	}
	cost := details.ActualCost()
	if cost == nil || *cost > MaxExpressionCost || e.cost > MaxEvaluationCost-*cost {
		return nil, ErrLimit
	}
	e.cost += *cost
	return value, nil
}

// Evaluate validates one JSON object, matches every enabled route, renders each
// match once, and returns a frozen sanitized snapshot for every bound Transporter.
// It performs no persistence or sending. Any error discards the whole result.
func (p *Plan) Evaluate(ctx context.Context, raw []byte) ([]domain.FrozenDelivery, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	event, err := DecodeEvent(raw)
	if err != nil {
		return nil, err
	}
	eval := evaluation{ctx: ctx, variables: map[string]any{"event": event}}
	results := make([]domain.FrozenDelivery, 0)
	ruleResults := map[domain.RevisionRef]bool{}
	totalBytes := 0
	for _, route := range p.routes {
		if !route.Enabled {
			continue
		}
		matched := true
		// Evaluate every referenced rule even after a false result: an error in
		// a later rule must still reject the event before acceptance.
		for _, rule := range route.Rules {
			matches, evaluated := ruleResults[rule]
			if !evaluated {
				value, err := eval.run(p.rules[rule])
				if err != nil {
					return nil, err
				}
				boolean, ok := value.(types.Bool)
				if !ok {
					return nil, ErrEvaluation
				}
				matches = bool(boolean)
				ruleResults[rule] = matches
			}
			if !matches {
				matched = false
			}
		}
		if !matched {
			continue
		}
		body, err := p.render(&eval, route.Template)
		if err != nil {
			return nil, err
		}
		totalBytes += len(body) * len(route.Transporters)
		if totalBytes > MaxTotalRenderBytes {
			return nil, ErrLimit
		}
		for _, transporter := range route.Transporters {
			frozen := p.transporters[transporter]
			frozen.HTTP.Headers = maps.Clone(frozen.HTTP.Headers)
			frozen.HTTP.SecretHeaders = maps.Clone(frozen.HTTP.SecretHeaders)
			results = append(results, domain.FrozenDelivery{
				Source:      domain.RevisionRef{ID: p.source.ID, Revision: p.source.Revision},
				Route:       domain.RevisionRef{ID: route.ID, Revision: route.Revision},
				Rules:       slices.Clone(route.Rules),
				Template:    route.Template,
				Transporter: frozen,
				Body:        bytes.Clone(body),
			})
		}
	}
	return results, nil
}

func (p *Plan) render(eval *evaluation, template domain.RevisionRef) ([]byte, error) {
	output := map[string]any{}
	// Reserve the outer object and Encoder's trailing LF before converting any
	// values. Encoder buffers the whole document before its first Write, so the
	// writer's limit alone cannot bound allocations from repeated references.
	budget := renderBudget{remaining: MaxRenderedBytes - 3}
	for i, field := range p.templates[template] {
		value, err := eval.run(field.program)
		if err != nil {
			return nil, err
		}
		if i > 0 {
			if err := budget.consume(1); err != nil { // Field separator.
				return nil, err
			}
		}
		if err := budget.consumeString(field.name); err != nil {
			return nil, err
		}
		if err := budget.consume(1); err != nil { // Key/value separator.
			return nil, err
		}
		native, err := jsonValue(eval.ctx, value, 0, &budget)
		if err != nil {
			return nil, err
		}
		output[field.name] = native
	}
	writer := new(renderBuffer)
	if err := json.NewEncoder(writer).Encode(output); err != nil {
		return nil, ErrLimit
	}
	return bytes.TrimSuffix(writer.Bytes(), []byte{'\n'}), nil
}

type renderBudget struct {
	remaining int
	nodes     int
}

func (b *renderBudget) consume(size int) error {
	if size > b.remaining {
		return ErrLimit
	}
	b.remaining -= size
	return nil
}

// consumeString counts encoding/json's default string representation without
// allocating it, including HTML escapes and invalid UTF-8 replacement. Charge
// every occurrence: several CEL values can refer to the same underlying string.
func (b *renderBudget) consumeString(value string) error {
	if b.remaining < 2 || len(value) > b.remaining-2 {
		return ErrLimit
	}
	if err := b.consume(2); err != nil { // Quotes.
		return err
	}
	for i := 0; i < len(value); {
		size, width := 1, 1
		switch c := value[i]; {
		case c == '"' || c == '\\' || c == '\b' || c == '\f' || c == '\n' || c == '\r' || c == '\t':
			size = 2
		case c < 0x20 || c == '<' || c == '>' || c == '&':
			size = 6
		case c >= utf8.RuneSelf:
			r, decodedWidth := utf8.DecodeRuneInString(value[i:])
			width, size = decodedWidth, decodedWidth
			if r == utf8.RuneError && width == 1 || r == '\u2028' || r == '\u2029' {
				size = 6
			}
		}
		if err := b.consume(size); err != nil {
			return err
		}
		i += width
	}
	return nil
}

type renderBuffer struct{ bytes.Buffer }

func (b *renderBuffer) Write(data []byte) (int, error) {
	if b.Len()+len(data) > MaxRenderedBytes {
		return 0, ErrLimit
	}
	return b.Buffer.Write(data)
}

// Explicit conversion rejects CEL-only types and nonstring map keys while
// preserving integer precision. Charge encoded bytes before building each part
// of the native value; encoding/json subsequently sorts output object keys.
func jsonValue(ctx context.Context, value ref.Val, depth int, budget *renderBudget) (any, error) {
	budget.nodes++
	if depth > MaxJSONDepth || budget.nodes > MaxJSONNodes || ctx.Err() != nil {
		return nil, ErrLimit
	}
	var scalar any
	switch value := value.(type) {
	case types.Null:
		scalar = nil
	case types.Bool:
		scalar = bool(value)
	case types.Int:
		scalar = int64(value)
	case types.Uint:
		scalar = uint64(value)
	case types.Double:
		if math.IsInf(float64(value), 0) || math.IsNaN(float64(value)) {
			return nil, ErrEvaluation
		}
		scalar = float64(value)
	case types.String:
		if err := budget.consumeString(string(value)); err != nil {
			return nil, err
		}
		return string(value), nil
	case traits.Lister:
		if err := budget.consume(2); err != nil { // Brackets.
			return nil, err
		}
		output := make([]any, 0)
		for iterator := value.Iterator(); iterator.HasNext() == types.True; {
			if len(output) > 0 {
				if err := budget.consume(1); err != nil { // Element separator.
					return nil, err
				}
			}
			child, err := jsonValue(ctx, iterator.Next(), depth+1, budget)
			if err != nil {
				return nil, err
			}
			output = append(output, child)
		}
		return output, nil
	case traits.Mapper:
		if err := budget.consume(2); err != nil { // Braces.
			return nil, err
		}
		output := map[string]any{}
		for iterator := value.Iterator(); iterator.HasNext() == types.True; {
			key, ok := iterator.Next().(types.String)
			if !ok {
				return nil, ErrEvaluation
			}
			if len(output) > 0 {
				if err := budget.consume(1); err != nil { // Field separator.
					return nil, err
				}
			}
			if err := budget.consumeString(string(key)); err != nil {
				return nil, err
			}
			if err := budget.consume(1); err != nil { // Key/value separator.
				return nil, err
			}
			child, err := jsonValue(ctx, value.Get(key), depth+1, budget)
			if err != nil {
				return nil, err
			}
			output[string(key)] = child
		}
		return output, nil
	default:
		return nil, ErrEvaluation
	}
	// Only fixed-size scalar types reach this encoding step. Strings and
	// containers were accounted for above without materializing their JSON.
	encoded, err := json.Marshal(scalar)
	if err != nil {
		return nil, ErrEvaluation
	}
	if err := budget.consume(len(encoded)); err != nil {
		return nil, err
	}
	return scalar, nil
}
