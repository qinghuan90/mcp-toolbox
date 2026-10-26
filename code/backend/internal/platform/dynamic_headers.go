package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

var headerNamePattern = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")

// parseHelperCommand accepts quoted command arguments, but never invokes a shell.
func parseHelperCommand(input string) ([]string, error) {
	if len(input) == 0 || len(input) > 8192 {
		return nil, errors.New("命令长度无效")
	}
	var args []string
	var part strings.Builder
	quote := byte(0)
	escaped, started := false, false
	for i := 0; i < len(input); i++ {
		c := input[i]
		if escaped {
			part.WriteByte(c)
			escaped = false
			started = true
			continue
		}
		if c == '\\' && quote != '\'' {
			escaped = true
			started = true
			continue
		}
		if quote != 0 {
			if c == quote {
				quote = 0
			} else {
				part.WriteByte(c)
			}
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
			started = true
			continue
		}
		if c == ' ' || c == '\t' || c == '\n' {
			if started {
				args = append(args, part.String())
				part.Reset()
				started = false
			}
			continue
		}
		part.WriteByte(c)
		started = true
	}
	if escaped || quote != 0 {
		return nil, errors.New("引号或转义不完整")
	}
	if started {
		args = append(args, part.String())
	}
	if len(args) == 0 || len(args) > 32 || args[0] == "" {
		return nil, errors.New("需要可执行命令和参数")
	}
	return args, nil
}

type boundedOutput struct{ bytes.Buffer }

func (b *boundedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 8192 {
		return 0, errors.New("动态请求头输出超过8KiB")
	}
	return b.Buffer.Write(p)
}

func runHeaderHelper(parent context.Context, helper string) (map[string]string, error) {
	args, err := parseHelperCommand(helper)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	if err := isolateProcess(cmd); err != nil {
		return nil, errors.New("当前系统不支持隔离动态请求头命令")
	}
	cmd.Env = platformProcessEnv(true)
	var output boundedOutput
	cmd.Stdout = &output
	err = cmd.Run()
	if cmd.Process != nil {
		terminateProcessGroup(cmd.Process.Pid)
	}
	if err != nil {
		return nil, errors.New("动态请求头生成失败，请检查命令与本机凭据")
	}
	var headers map[string]string
	if err := json.Unmarshal(output.Bytes(), &headers); err != nil || len(headers) == 0 || len(headers) > 20 {
		return nil, errors.New("动态请求头须输出包含1至20个字段的 JSON 对象")
	}
	for key, value := range headers {
		if !headerNamePattern.MatchString(key) || strings.ContainsAny(value, "\r\n") || len(value) > 4096 {
			return nil, errors.New("动态请求头包含无效字段")
		}
	}
	return headers, nil
}
