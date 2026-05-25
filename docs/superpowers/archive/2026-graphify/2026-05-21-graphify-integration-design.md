# graphify 통합 — 설계 명세

- **작성일**: 2026-05-21
- **대상 코드베이스**: `MD_Viewer` (Go 1.22, web.go / menubar.go 구조)
- **대상 사용자**: 마크다운 노트를 MD Viewer로 읽으면서, 노트 간 개념적 연관성을 함께 탐색하고 싶은 사용자
- **선행 도구**: `graphify` (`pip install graphifyy`) — 별도 설치된 CLI

## 1. 한 줄 요약

> MD Viewer 루트 폴더에 존재하는 `graphify-out/graph.json`을 읽어, 현재 보고 있는 파일에서 추출된 개념을 우측 사이드바에 칩으로 표시하고, 칩을 누르면 그 개념이 등장하는 다른 파일로 이동할 수 있게 한다. 그래프가 없으면 같은 자리에서 빌드를 트리거할 수 있다.

## 2. 동기

MD Viewer는 현재 "지금 보고 있는 파일"에만 집중된 뷰어다. 노트가 늘어날수록 사용자는 "이 주제가 어디서 또 나왔지?"를 머릿속이나 파일명 검색으로 풀어야 한다. graphify가 만든 지식 그래프를 활용하면 파일명 매칭이 아닌 **개념 매칭**으로 노트 사이를 이동할 수 있다 — Obsidian의 backlinks 와 비슷한 경험을 LLM 추출 기반으로.

## 3. 비-목표 (YAGNI)

다음 항목은 **이번 명세 범위 밖**이다. 추가 요구가 생기면 별도 명세를 작성한다.

- 그래프 자체의 시각화 (이미 `graph.html` 산출물이 있으니 새 탭 링크만 노출)
- 자연어 질의 (`graphify query`) UI
- 그래프 편집 / 노드 추가 / 즐겨찾기
- 동시에 여러 vault의 그래프 (MVP는 `--root` 단일 그래프)
- TUI 모드 통합 (Web / Menubar 모드 전용)
- 외부 vault 경로 지정 (`--graph <path>` 같은 CLI 플래그) — MVP는 항상 `<root>/graphify-out/graph.json`

## 4. 단계 분할

### 4.1 1단계 — 그래프 소비 (Consumer)

- `graphify-out/graph.json`을 읽고, 현재 파일의 칩 + 칩 클릭 시 연결 파일 목록을 보여준다.
- **빌드는 포함하지 않는다.** 그래프가 없으면 "터미널에서 `graphify .` 를 실행하세요" 안내문만 표시한다.
- 그래프 파일이 디스크에서 변경되면 자동 리로드 (mtime 비교).

### 4.2 2단계 — 그래프 빌드 (Builder)

- 우측 패널에 **Build graph** 버튼.
- 버튼 클릭 → 백엔드가 `graphify <root>` 를 비동기 서브프로세스로 실행.
- 진행 상태는 SSE(`/api/graph/build/status`)로 스트리밍.
- 완료 시 1단계 인덱서가 자동으로 새 `graph.json`을 로드 (mtime 변경 감지).
- 빌드 중 다른 요청은 거부 (단일 동시 빌드).

각 단계는 독립적으로 사용 가능하다. 1단계만 머지하더라도 사용자는 터미널에서 graphify를 돌린 뒤 MD Viewer에서 결과를 활용할 수 있다.

## 5. 아키텍처

```
┌─ Frontend (web.go HTML/JS) ─────────────────────────┐
│                                                      │
│  ┌─ Left Sidebar ─┐  ┌─ Preview ─┐ ┌─ Graph Rail ─┐│
│  │ (기존 그대로)   │  │ (기존)      │ │ 신규         ││
│  │                 │  │            │ │  Concepts    ││
│  │                 │  │            │ │  [chip][chip]││
│  │                 │  │            │ │  ─────────── ││
│  │                 │  │            │ │  Linked      ││
│  │                 │  │            │ │   files      ││
│  │                 │  │            │ │  · a.md      ││
│  │                 │  │            │ │  · b.md      ││
│  └─────────────────┘  └────────────┘ └──────────────┘│
└──────────────────────────────────────────────────────┘
        │                                  │
        ▼ HTTP                              ▼ HTTP
┌─ Backend (Go) ──────────────────────────────────────┐
│  graph.go         (인덱서: load + lookup)            │
│  graph_build.go   (2단계: graphify CLI orchestrator) │
│  web.go           (4개 신규 라우트)                  │
└──────────────────────────────────────────────────────┘
        │
        ▼ read on demand + mtime stat per request
   <root>/graphify-out/graph.json
```

## 6. 컴포넌트

### 6.1 `graph.go` (신규 파일, ~150 LOC) — 1단계

**책임:** `graph.json` 파싱, 인덱싱, 조회.

**공개 API (패키지 내):**

```go
type GraphIndex struct {
    // 기능적으로 immutable; mtime 변경 시 통째로 교체
    nodes     map[string]Node       // id → node
    byFile    map[string][]string   // 절대경로 → []id
    neighbors map[string][]string   // id → 연결된 노드 id
    loadedAt  time.Time
    sourcePath string
}

type Node struct {
    ID        string `json:"id"`
    Label     string `json:"label"`
    FileType  string `json:"file_type"`   // code|document|paper|image|...
    SourceFile string `json:"source_file"`
}

func LoadGraph(jsonPath string, projectRoot string) (*GraphIndex, error)
func (g *GraphIndex) ConceptsInFile(absPath string) []Node
func (g *GraphIndex) FilesForConcept(nodeID string) []FileRef  // 노드 자체의 source_file + 이웃 노드들의 source_file
type FileRef struct { Path string; Label string; FileType string }
```

**핵심 규칙:**
- `graph.json`의 `nodes[i].source_file` 는 상대/절대경로 혼재 가능 → 인덱싱 시 `projectRoot` 기준으로 절대경로 정규화.
- `LoadGraph`는 동기적; 호출자(서버)가 적절한 시점에 호출.
- 멤버 필드 모두 unexported, 메서드만 노출.

### 6.2 `webServer` 멤버 추가

```go
type webServer struct {
    startDir string
    appRoot  string
    // 신규
    graphMu     sync.RWMutex
    graph       *GraphIndex   // nil이면 그래프 없음
    graphPath   string        // <root>/graphify-out/graph.json
    graphMTime  time.Time
}
```

`runWebServer`에서 `graphPath`를 설정하고 최초 로드 시도. 실패는 무시 (없을 수 있으니).

### 6.3 라우트 (web.go, 1단계 기준)

| Method | Path | 응답 | 비고 |
|---|---|---|---|
| GET | `/api/graph/status` | `{available: bool, nodeCount: int, lastBuilt: RFC3339}` | 항상 200 |
| GET | `/api/graph/file?path=...` | `[{id, label, file_type}, ...]` | path는 절대경로. 그래프 없거나 매칭 없으면 `[]` |
| GET | `/api/graph/concept?id=...` | `[{path, label, file_type}, ...]` | 노드 미존재 시 404 |

**리로드 로직:** 각 요청 진입 시 `graphPath`의 mtime을 stat → `graphMTime`보다 새로우면 lock 잡고 다시 `LoadGraph`. 폴링 부담은 stat 한 번이라 무시 가능.

### 6.4 라우트 (2단계 추가)

| Method | Path | 응답 |
|---|---|---|
| POST | `/api/graph/build` | `{jobId: string}` 또는 409 (이미 빌드 중) |
| GET | `/api/graph/build/status` | SSE: `data: {phase, progress, message}\n\n` |

`graph_build.go`:
- `graphify` 바이너리 위치를 `exec.LookPath`로 확인. 없으면 빌드 요청 거부 + 안내.
- 환경 변수 `GEMINI_API_KEY` 또는 `GOOGLE_API_KEY` 확인. 없으면 거부 (Claude Code 서브에이전트 모드는 백엔드에서 호출 불가).
- 빌드 명령: `graphify <root>` (전체 파이프라인). stdout/stderr 라인 단위 파싱해서 phase 추정.
- 단일 빌드 제한: `sync.Mutex` `buildMu`. 잠겨 있으면 409.
- 빌드 완료 후 `LoadGraph`를 직접 호출해 인메모리 인덱스 즉시 교체.

### 6.5 프론트엔드 (`webAppHTML` 내, ~200 LOC)

**레이아웃 변경:**
- 기존 `.app` grid: `sidebar | splitter | preview` → `sidebar | splitter | preview | graph-rail` (우측 240px)
- `--graph-rail-width: 240px` CSS 변수 추가
- 사이드바 콜랩스처럼 그래프 패널도 토글 가능 (`graph-rail-collapsed` 클래스)

**행위:**
- 파일 선택 시점에 `/api/graph/file?path=<abs>` 함께 호출.
- 응답 노드 목록을 칩으로 렌더. 칩 클래스에 `data-id="<nodeID>"`, 색상은 file_type별 — `document` / `code` / `paper` / `image` / `concept` / `rationale` 각각 다른 톤 (테마 토큰 활용).
- 칩 클릭 → `/api/graph/concept?id=<id>` 호출, "Linked files" 섹션에 결과 렌더. 클릭 시 기존 `selectFile()` 재사용.
- `/api/graph/status` 응답이 `available: false` → 패널 자리에:
  - 1단계: 안내문 "Run `graphify .` in this folder to enable concept search."
  - 2단계: 그 안내문 + **Build graph** 버튼.

## 7. 데이터 흐름 (정상 케이스)

```
[사용자가 README.md 클릭]
  │
  ├── GET /api/file?path=...               (기존)
  │      → 본문 → 미리보기 렌더
  │
  └── GET /api/graph/file?path=/abs/README.md   (신규)
         → [{id:"mdviewer_main_model", label:"model", ...}, ...]
         → 칩 렌더

[사용자가 "model" 칩 클릭]
  └── GET /api/graph/concept?id=mdviewer_main_model
         → [{path:"/abs/main.go", label:"model", ...}, ...]
         → "Linked files" 리스트 렌더

[사용자가 "main.go" 클릭]
  └── selectFile("main.go")  (기존 함수 재사용)
       → 1번 흐름 반복
```

## 8. 에러 / 엣지 케이스

| 상황 | 동작 |
|---|---|
| `graph.json` 부재 | 패널: 빌드 안내문 (+ 2단계: 버튼) |
| `graph.json` 파싱 실패 | 패널: "Graph file corrupted: <err>" + Reload 버튼 |
| 파일에 추출된 노드 0개 | 칩 영역: subtle "No concepts extracted" |
| 노드 ID 미존재 (`/api/graph/concept`) | 404 + 클라이언트는 "Concept not found" |
| `source_file` 상대/절대 혼재 | `LoadGraph`에서 모두 절대경로로 정규화 |
| `graphify` 미설치 (2단계) | `/api/graph/build` 응답 503 + "graphify CLI not found. pip install graphifyy" |
| `GEMINI_API_KEY` 미설정 (2단계) | 503 + 안내 |
| 빌드 중 다른 빌드 요청 (2단계) | 409 + 현재 jobId 반환 |
| 빌드 도중 graphify 비정상 종료 (2단계) | SSE 종료 + `phase: error, message: <stderr 마지막 줄>` |
| 그래프 mtime은 변했는데 파일 락 걸려 있음 | stat 실패 시 기존 인덱스 유지, 다음 요청에서 재시도 |
| 절대경로가 OS 심볼릭 링크 해소와 불일치 | `filepath.EvalSymlinks`를 인덱싱·조회 양쪽에 동일 적용 |

## 9. 테스트 전략

### 1단계 (graph.go)
- **단위**: 가짜 `graph.json` 파일을 testdata에 두고 `LoadGraph` → `ConceptsInFile`, `FilesForConcept` 결과 검증.
- 상대경로/절대경로 혼재 케이스 포함.
- 노드 0개, 엣지 0개, 빈 그래프 케이스.

### 1단계 (web.go)
- `httptest.NewServer`로 라우트 통합 테스트.
- 그래프 부재 → `/api/graph/status` 가 `available:false`.
- 정상 그래프 → `/api/graph/file` 응답 검증.
- 미존재 nodeID → 404.

### 2단계 (graph_build.go)
- `graphify` 호출은 모킹 (PATH 조작으로 더미 스크립트 주입).
- 단일 동시 빌드 — 두 번째 요청 409 확인.
- SSE 응답 파싱 가능 형식 확인.

## 10. 작업량 예상

| 영역 | 라인 수 | 단계 |
|---|---|---|
| `graph.go` 신규 | ~150 | 1 |
| `web.go` 라우트 (3개) | ~70 | 1 |
| `web.go` HTML/CSS/JS | ~160 | 1 |
| 1단계 단위·통합 테스트 | ~120 | 1 |
| **1단계 합계** | **~500 LOC** | |
| `graph_build.go` 신규 | ~120 | 2 |
| `web.go` 라우트 (2개) + SSE | ~80 | 2 |
| `web.go` Build UI | ~60 | 2 |
| 2단계 테스트 | ~80 | 2 |
| **2단계 합계** | **~340 LOC** | |

## 11. 보안·프라이버시 메모

- 그래프 파일은 사용자의 로컬 디스크에만 존재; 새 네트워크 호출은 graphify 빌드 시 LLM API뿐.
- 빌드 API는 항상 `127.0.0.1` 바운드(기존 정책 그대로)이므로 외부 노출 위험 없음.
- `/api/graph/file?path=...`은 절대경로를 받는다 — 기존 `/api/file` 핸들러의 경로 검증 로직(루트 밖 거부)을 동일 적용한다.

## 12. 향후 확장 (이번 명세 외)

설계할 때 막지 않을 것:
- 외부 vault 경로 지정 (`--graph` 플래그 또는 설정 파일)
- 자연어 질의(`graphify query`) 패널 추가
- 멀티 그래프 (탭 형태)
- 그래프 노드 → 라인 단위 점프 (현재 source_location 필드 미사용)

각각 별도 명세로 다룬다.
