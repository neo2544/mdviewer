# mdviewer — TUI Markdown File Viewer

파일 탐색 + Markdown 렌더링을 지원하는 터미널 앱입니다.  
**bubbletea + lipgloss + glamour** (charmbracelet 생태계)로 만들어졌습니다.

## 구조

```
mdviewer/
├── main.go      # 전체 앱 (단일 파일)
├── go.mod
└── go.sum
```

## 설치 & 실행

```bash
# 1. 의존성 설치
go mod tidy

# 2. 현재 디렉터리로 실행
go run .

# 3. 특정 디렉터리 지정
go run . ~/Documents

# 4. 빌드 후 실행
go build -o mdviewer .
./mdviewer ~/projects
```

## 키보드 단축키

| 키 | 동작 |
|---|---|
| `↑` / `k` | 목록 위로 / 미리보기 스크롤 위 |
| `↓` / `j` | 목록 아래로 / 미리보기 스크롤 아래 |
| `Enter` | 디렉터리 열기 |
| `Tab` | 목록 ↔ 미리보기 포커스 전환 |
| `PgUp` / `PgDn` | 미리보기 반 페이지 스크롤 |
| `Home` / `End` | 미리보기 맨 위/아래 |
| `q` / `Ctrl+C` | 종료 |

## 미리보기 지원 형식

- **Markdown** (`.md`, `.markdown`, `.mdx`) — glamour로 렌더링
- **텍스트/코드** (`.txt`, `.go`, `.py`, `.js`, `.ts`, `.sh`, `.yaml`, `.json`, `.toml`) — 원문 표시
- 기타 바이너리 파일 — 미지원 안내

## 화면 구성

```
┌─ 📄 MD Viewer  /your/path ────────────────────────────────────┐
│ ╭──────────────╮ ╭──────────────────────────────────────────╮ │
│ │ ..           │ │                                          │ │
│ │ 📁 docs/     │ │   # Hello World                          │ │
│ │ 📁 src/      │ │                                          │ │
│ │▶ README.md   │ │   This is a **markdown** file rendered   │ │
│ │ main.go      │ │   beautifully in your terminal.          │ │
│ │ go.mod       │ │                                          │ │
│ ╰──────────────╯ ╰──────────────────────────────────────────╯ │
│  Markdown — Tab to switch pane                                 │
│  q quit • ↑↓ navigate • Enter open dir • Tab switch pane      │
└────────────────────────────────────────────────────────────────┘
```

## 의존 패키지

```
github.com/charmbracelet/bubbletea  — TUI 프레임워크 (Elm architecture)
github.com/charmbracelet/bubbles    — viewport 컴포넌트
github.com/charmbracelet/lipgloss   — 스타일링
github.com/charmbracelet/glamour    — Markdown 렌더링
```
