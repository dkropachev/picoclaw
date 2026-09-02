package sqlbridge

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/database"
)

const (
	// MaxStatementBytes bounds both runtime statements and offline schema work.
	MaxStatementBytes  = 1 << 20
	maxStatementTokens = 131072
)

type sqlTokenKind uint8

const (
	sqlWord sqlTokenKind = iota + 1
	sqlNumber
	sqlString
	sqlQuotedIdentifier
	sqlSymbol
)

type sqlToken struct {
	kind sqlTokenKind
	text string
}

// ValidateStatement applies the closed Matrix/WhatsApp bridge policy to one
// SQL statement. Lexing is aware of SQLite comments and quoting, so keywords or
// semicolons inside them cannot change the classification.
func ValidateStatement(statement string, mode Mode) error {
	if !mode.Valid() {
		return invalidStatement()
	}
	tokens, err := tokenizeStatement(statement)
	if err != nil {
		return err
	}
	tokens, err = oneStatement(tokens)
	if err != nil {
		return err
	}
	if len(tokens) == 0 || tokens[0].kind != sqlWord {
		return invalidStatement()
	}

	head := strings.ToUpper(tokens[0].text)
	switch head {
	case "SELECT", "VALUES", "INSERT", "UPDATE", "DELETE", "REPLACE":
		return nil
	case "WITH":
		if len(tokens) < 2 {
			return invalidStatement()
		}
		return nil
	case "PRAGMA":
		return validatePragma(tokens, mode)
	case "CREATE", "ALTER", "DROP":
		if mode != ModeOffline {
			return unsupportedStatement()
		}
		return validateOfflineDDL(tokens)
	case "ATTACH", "DETACH", "VACUUM", "BEGIN", "COMMIT", "END", "ROLLBACK",
		"SAVEPOINT", "RELEASE", "ANALYZE", "REINDEX":
		return unsupportedStatement()
	default:
		return unsupportedStatement()
	}
}

// StatementMayMutate conservatively classifies a policy-valid statement for
// transport ambiguity. WITH is treated as mutating because its top-level CTE
// may contain INSERT/UPDATE/DELETE with RETURNING.
func StatementMayMutate(statement string, mode Mode) bool {
	if ValidateStatement(statement, mode) != nil {
		return false
	}
	tokens, err := tokenizeStatement(statement)
	if err != nil {
		return false
	}
	tokens, err = oneStatement(tokens)
	if err != nil || len(tokens) == 0 {
		return false
	}
	switch strings.ToUpper(tokens[0].text) {
	case "SELECT", "VALUES", "PRAGMA":
		return false
	default:
		return true
	}
}

func oneStatement(tokens []sqlToken) ([]sqlToken, error) {
	semicolon := -1
	for index, token := range tokens {
		if token.kind != sqlSymbol || token.text != ";" {
			continue
		}
		if semicolon >= 0 || index != len(tokens)-1 {
			return nil, unsupportedStatement()
		}
		semicolon = index
	}
	if semicolon >= 0 {
		tokens = tokens[:semicolon]
	}
	if len(tokens) == 0 {
		return nil, invalidStatement()
	}
	return tokens, nil
}

func validateOfflineDDL(tokens []sqlToken) error {
	if len(tokens) < 3 {
		return invalidStatement()
	}
	head := strings.ToUpper(tokens[0].text)
	switch head {
	case "CREATE":
		index := 1
		if tokenWord(tokens, index, "UNIQUE") {
			index++
			if !tokenWord(tokens, index, "INDEX") {
				return unsupportedStatement()
			}
			return nil
		}
		if tokenWord(tokens, index, "TABLE") || tokenWord(tokens, index, "INDEX") {
			return nil
		}
	case "ALTER":
		if tokenWord(tokens, 1, "TABLE") {
			return nil
		}
	case "DROP":
		if tokenWord(tokens, 1, "TABLE") || tokenWord(tokens, 1, "INDEX") {
			return nil
		}
	}
	return unsupportedStatement()
}

func validatePragma(tokens []sqlToken, mode Mode) error {
	if len(tokens) < 2 {
		return invalidStatement()
	}
	index := 1
	name, ok := identifierToken(tokens[index])
	if !ok {
		return invalidStatement()
	}
	index++
	if index < len(tokens) && tokens[index].kind == sqlSymbol && tokens[index].text == "." {
		if !strings.EqualFold(name, "main") || index+1 >= len(tokens) {
			return unsupportedStatement()
		}
		name, ok = identifierToken(tokens[index+1])
		if !ok {
			return invalidStatement()
		}
		index += 2
	}
	name = strings.ToLower(name)
	rest := tokens[index:]

	switch name {
	case "foreign_keys", "schema_version", "application_id":
		if len(rest) == 0 {
			return nil
		}
		return unsupportedStatement()
	case "user_version":
		if len(rest) == 0 {
			return nil
		}
		if mode != ModeOffline || len(rest) != 2 || rest[0].kind != sqlSymbol ||
			rest[0].text != "=" || rest[1].kind != sqlNumber {
			return unsupportedStatement()
		}
		value, err := strconv.ParseUint(rest[1].text, 10, 31)
		if err != nil || value > 1<<31-1 {
			return invalidStatement()
		}
		return nil
	case "table_info", "table_xinfo", "index_list", "index_info", "index_xinfo",
		"foreign_key_list":
		if len(rest) == 0 {
			return nil
		}
		if len(rest) != 3 || rest[0].kind != sqlSymbol || rest[0].text != "(" ||
			rest[2].kind != sqlSymbol || rest[2].text != ")" || !pragmaArgument(rest[1]) {
			return unsupportedStatement()
		}
		return nil
	default:
		return unsupportedStatement()
	}
}

func pragmaArgument(token sqlToken) bool {
	switch token.kind {
	case sqlWord, sqlString, sqlQuotedIdentifier:
		return token.text != "" && len(token.text) <= 512
	default:
		return false
	}
}

func tokenWord(tokens []sqlToken, index int, expected string) bool {
	return index >= 0 && index < len(tokens) && tokens[index].kind == sqlWord &&
		strings.EqualFold(tokens[index].text, expected)
}

func identifierToken(token sqlToken) (string, bool) {
	if token.kind != sqlWord && token.kind != sqlQuotedIdentifier {
		return "", false
	}
	return token.text, token.text != ""
}

func tokenizeStatement(statement string) ([]sqlToken, error) {
	if statement == "" || len(statement) > MaxStatementBytes || !utf8.ValidString(statement) ||
		strings.IndexByte(statement, 0) >= 0 {
		return nil, invalidStatement()
	}
	tokens := make([]sqlToken, 0, min(len(statement)/4+1, 1024))
	for index := 0; index < len(statement); {
		character := statement[index]
		if sqlSpace(character) {
			index++
			continue
		}
		if character == '-' && index+1 < len(statement) && statement[index+1] == '-' {
			index += 2
			for index < len(statement) && statement[index] != '\n' && statement[index] != '\r' {
				index++
			}
			continue
		}
		if character == '/' && index+1 < len(statement) && statement[index+1] == '*' {
			end := strings.Index(statement[index+2:], "*/")
			if end < 0 {
				return nil, invalidStatement()
			}
			index += end + 4
			continue
		}
		if character == '\'' || character == '"' || character == '`' {
			text, next, ok := scanSQLQuote(statement, index, character)
			if !ok {
				return nil, invalidStatement()
			}
			kind := sqlQuotedIdentifier
			if character == '\'' {
				kind = sqlString
			}
			tokens = append(tokens, sqlToken{kind: kind, text: text})
			index = next
		} else if character == '[' {
			end := strings.IndexByte(statement[index+1:], ']')
			if end < 0 {
				return nil, invalidStatement()
			}
			end += index + 1
			tokens = append(tokens, sqlToken{kind: sqlQuotedIdentifier, text: statement[index+1 : end]})
			index = end + 1
		} else if sqlDigit(character) {
			start := index
			for index < len(statement) && sqlDigit(statement[index]) {
				index++
			}
			tokens = append(tokens, sqlToken{kind: sqlNumber, text: statement[start:index]})
		} else if sqlWordStart(character) {
			start := index
			for index < len(statement) && sqlWordContinue(statement[index]) {
				index++
			}
			tokens = append(tokens, sqlToken{kind: sqlWord, text: statement[start:index]})
		} else {
			tokens = append(tokens, sqlToken{kind: sqlSymbol, text: statement[index : index+1]})
			index++
		}
		if len(tokens) > maxStatementTokens {
			return nil, invalidStatement()
		}
	}
	return tokens, nil
}

func scanSQLQuote(statement string, start int, delimiter byte) (string, int, bool) {
	var value strings.Builder
	for index := start + 1; index < len(statement); index++ {
		if statement[index] != delimiter {
			value.WriteByte(statement[index])
			continue
		}
		if index+1 < len(statement) && statement[index+1] == delimiter {
			value.WriteByte(delimiter)
			index++
			continue
		}
		return value.String(), index + 1, true
	}
	return "", 0, false
}

func sqlSpace(character byte) bool {
	return character == ' ' || character == '\t' || character == '\n' ||
		character == '\r' || character == '\f'
}

func sqlDigit(character byte) bool { return character >= '0' && character <= '9' }

func sqlWordStart(character byte) bool {
	return character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
		character == '_' || character >= utf8.RuneSelf
}

func sqlWordContinue(character byte) bool {
	return sqlWordStart(character) || sqlDigit(character) || character == '$'
}

func invalidStatement() error {
	return database.NewError(database.CodeInvalid, "SQL bridge statement is invalid")
}

func unsupportedStatement() error {
	return database.NewError(database.CodeUnsupported, "SQL bridge statement is not permitted")
}
