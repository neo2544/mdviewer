# mdviewer — Web · Menubar · TUI Markdown Viewer

브라우저 기반 웹 뷰어 + macOS 메뉴바 자동 실행 + 터미널 TUI까지, 한 바이너리에서 세 가지 모드로 마크다운을 탐색·편집할 수 있는 뷰어입니다.

- **Web (기본 권장)** — `http://127.0.0.1:8421/` — 사이드바 탐색, Edit/Split/Preview, Mermaid 라이트박스(주석/포스트잇/PNG 저장), 검색, 즐겨찾기, 테마 등
- **Menubar** — 로그인 시 자동 시작, 메뉴바에서 클릭 한 번에 브라우저로 열기 + `.md` 더블클릭 연동
- **TUI** — `bubbletea + glamour` 기반 터미널 모드

## 🚀 빠른 설치 (메뉴바 + 자동 시작 + `.md` 연동)

macOS에서 한 번에 설치하기:

```bash
# 1. 저장소 클론
git clone https://github.com/neo2544/mdviewer.git
cd mdviewer

# 2. 설치 — 이 한 줄이면 끝
scripts/install.sh
```

이렇게 하면:
- ✅ `mdviewer` 바이너리 빌드 (Xcode CLT만 있으면 OK)
- ✅ `~/Applications/MdViewer.app` 메뉴바 앱으로 등록 (Dock에는 안 뜸)
- ✅ 로그인 시 자동 시작 + 죽으면 자동 재시작 (LaunchAgent)
- ✅ `.md` / `.markdown` / `.mdx` 파일 더블클릭 → 바로 브라우저에서 열림
- ✅ `http://127.0.0.1:8421/` 가 항상 떠 있음

설치 옵션:
```bash
scripts/install.sh --root ~/Documents --port 8421   # 다른 폴더/포트
scripts/install.sh --help                            # 전체 옵션
scripts/uninstall.sh                                 # 제거
```

설치 후 메뉴바 아이콘(M↓)을 클릭하면:

![Menu bar dropdown showing MD Viewer options](assets/menubar-screenshot.png)

- **Open in Browser** — 웹 뷰어 열기
- **Reveal Root Folder in Finder**
- **Copy URL**
- **Quit**

로그: `~/Library/Logs/MdViewer/mdviewer.{out,err}.log`

---

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
fyne.io/systray                     — macOS 메뉴바 아이콘 (menubar 모드)
```

## 실행 모드

| 모드 | 명령 | 설명 |
|---|---|---|
| TUI | `mdviewer [path]` | 터미널 안에서 직접 탐색 |
| Web | `mdviewer --web [--port 8421] [--root <dir>]` | 로컬 웹 서버, 브라우저에서 사용 |
| Menubar | `mdviewer --menubar [--port 8421] [--root <dir>]` | macOS 메뉴바 아이콘 + 웹 서버 |

`g` 또는 `:` 키 → 경로 점프 모드 (TUI). 절대/상대/`~` 경로 입력 후 Enter → 해당 디렉토리로 이동하고 파일이면 선택까지. 웹 모드에서는 사이드바의 "Jump to path" 입력 또는 `Cmd/Ctrl+L`.

## macOS 백그라운드 설치 (메뉴바 + 자동 시작 + .md 연동)

```bash
scripts/install.sh                                     # 기본: 이 폴더를 루트로
scripts/install.sh --root ~/Documents --port 8421      # 다른 폴더/포트로
scripts/uninstall.sh                                   # 제거
```

`install.sh`가 하는 일:

1. `mdviewer` 바이너리 빌드 (CGO 필요 — Xcode CLT가 있으면 OK)
2. `~/Applications/MdViewer.app` 번들 생성 — `LSUIElement=true` 라 Dock에는 안 뜸, 메뉴바에만 표시
3. `CFBundleDocumentTypes`로 `.md / .markdown / .mdx` 파일 핸들러 등록 (`lsregister`로 즉시 반영)
4. `~/Library/LaunchAgents/com.jk.mdviewer.plist` LaunchAgent 작성 — 로그인 시 자동 시작 + 죽으면 재시작
5. `launchctl bootstrap` 으로 즉시 로드

설치 후:

- **메뉴바 아이콘** 클릭 → Open in Browser / Reveal Root Folder / Copy URL / Quit
- **`.md` 파일 우클릭 → 다음으로 열기 → MD Viewer** → 브라우저에서 해당 파일이 바로 열림 (Apple Event `kAEOpenDocuments` 핸들러로 처리)
- **`http://127.0.0.1:8421/`** 가 항상 활성 — 즐겨찾기로 두면 편함
- 로그: `~/Library/Logs/MdViewer/mdviewer.{out,err}.log`

## 버전 & 자가 업데이트

- 헤더 우측과 왼쪽 사이드바 하단에 **현재 버전**(브랜치 + 짧은 커밋 해시)이 표시됩니다.
- repo 안에서 빌드한 바이너리로 실행하면, 원격(origin)에 새 커밋이 있을 때 **`⬆ Update (N)`** 버튼이 나타납니다. 누르면 `git pull --ff-only → go build → 새 바이너리로 자동 재시작`까지 처리합니다.
- `install.sh`로 설치한 메뉴바 앱은 빌드 시점 버전을 표시하지만 자가 업데이트는 비활성입니다 — 갱신은 `git pull && scripts/install.sh`로 재설치하세요.
- `go run`(`run.sh`/`run-web.sh`)으로 실행하면 버전은 표시되되 자가 업데이트는 비활성입니다.
