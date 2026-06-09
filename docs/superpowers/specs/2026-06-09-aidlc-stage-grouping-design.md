# AI-DLC 단계별 번호·그룹 보기 — 설계

작성일: 2026-06-09

## 배경 / 목표

MD Viewer의 AI-DLC 모드는 `<gitroot>/aidlc-docs/` 아래 모든 문서를 **최근 수정순
평면 목록**으로만 보여준다. AI-DLC가 단계별로 많은 문서를 생성하는데 번호·구분이 없어
어떤 단계의 문서인지 알기 어렵다.

목표: AI-DLC 워크플로(awslabs/aidlc-workflows)의 정식 단계 구조에 맞춰 문서를
**단계별 번호(1·2 식)로 묶고 그룹 헤더로 구분**해서 보여주는 정렬 옵션을 추가한다.
아직 문서가 없는 단계는 **회색 placeholder**로 표시해 전체 진행 상황을 한눈에 보게 한다.

전부 프론트엔드 작업이다. `/api/aidlc`가 이미 `aidlc-docs` 기준 상대경로(`f.name`)를
주므로 **백엔드 변경은 없다**.

## 결정 사항 (확정)

| 항목 | 결정 |
|------|------|
| 번호 체계 | 단계.스텝 고정 (1·2). 문서 유무와 무관하게 카논 위치 고정 |
| 그룹 표시 | 페이즈 헤더(🔵/🟢/🟡 색 구분) + 스텝 서브그룹, 문서명 그룹화 |
| 적용 방식 | AI-DLC 모드 내 정렬 옵션 `단계순 ↔ 최근수`, 기본=단계순 |
| 회색 placeholder | 스텝 단위. 정식 스텝 13개 항상 표시, 빈 스텝은 회색 "(아직 없음)" |

## 정식 분류 매핑 (핵심)

파일의 `aidlc-docs` 상대경로를 `{phase, step}`으로 분류한다. 출처: 다이어그램 +
`awslabs/aidlc-workflows/docs/GENERATED_DOCS_REFERENCE.md`.

### 🔵 Inception (phase 1)
| 번호 | 스텝 키 / 라벨 | 경로 매칭 규칙 |
|---|---|---|
| 1·1 | `workspace` / Workspace Detection | `aidlc-docs/` 루트 직속 파일 (`aidlc-state.md`, `audit.md`, 기타 루트 파일) |
| 1·2 | `reverse` / Reverse Engineering | 1번째 세그먼트 `inception`, 2번째 `reverse-engineering` |
| 1·3 | `requirements` / Requirements Analysis | `inception/requirements/…` |
| 1·4 | `stories` / User Stories | `inception/user-stories/…` |
| 1·5 | `planning` / Workflow Planning | `inception/plans/…` |
| 1·6 | `appdesign` / Application Design | `inception/application-design/…` 且 파일명이 `unit-of-work`로 시작하지 **않음** |
| 1·7 | `units` / Units Generation | `inception/application-design/unit-of-work*` |

### 🟢 Construction (phase 2) — per-unit 루프 `construction/{unit}/…`
| 번호 | 스텝 키 / 라벨 | 경로 매칭 규칙 |
|---|---|---|
| 2·1 | `functional` / Functional Design | `construction/{unit}/functional-design/…` 또는 `construction/plans/*-functional-design-plan.md` |
| 2·2 | `nfrreq` / NFR Requirements | `construction/{unit}/nfr-requirements/…` 또는 `construction/plans/*-nfr-requirements-plan.md` |
| 2·3 | `nfrdesign` / NFR Design | `construction/{unit}/nfr-design/…` 또는 `plans/*-nfr-design-plan.md` |
| 2·4 | `infra` / Infrastructure Design | `construction/{unit}/infrastructure-design/…` 또는 `plans/*-infrastructure-design-plan.md` |
| 2·5 | `codegen` / Code Generation | `construction/{unit}/code/…` 또는 `plans/*-code-generation-plan.md` |
| 2·6 | `buildtest` / Build & Test | `construction/build-and-test/…` |

추가(비-placeholder, 존재 시에만): `construction/shared-infrastructure.md` →
Construction의 **2·0 공통 인프라**.

### 🟡 Operations (phase 3)
| 번호 | 스텝 키 / 라벨 | 경로 매칭 규칙 |
|---|---|---|
| 3·1 | `operations` / Operations (향후 제공) | `operations/…` (현재 카논 문서 없음 → 항상 회색 placeholder) |

### 기타
위 규칙에 안 맞는 모든 문서(예: `adr/ADR.md`, `review/phase1-*.md`)는 맨 아래
**"기타" 그룹**(있을 때만 표시, 번호 없음).

### 분류 함수
```
aidlcClassify(relPath) -> { phaseNum, phaseKey, phaseLabel, phaseEmoji,
                            stepNum, stepKey, stepLabel, order }
```
- 순수 함수. `relPath`를 `/`로 분할해 규칙 적용.
- `order` = phaseNum*100 + stepNum (그룹/스텝 정렬용). 기타는 order 9999.
- `application-design` 폴더는 Application Design/Units Generation이 같은 폴더라
  **파일명(leaf)**으로 1·6 / 1·7 구분.
- `construction/plans/`의 계획 문서는 **파일명 접미사**(`-functional-design-plan` 등)로
  해당 스텝에 매핑.

### 실제 데이터 검증 (son-local-env/aidlc-docs)
- 루트 `aidlc-state.md`/`audit.md` → 1·1. `requirements/`(여분 문서 포함) → 1·3,
  `user-stories/` → 1·4, `plans/` → 1·5, `application-design/`(api-design,
  data-model, interface-catalog 등 여분 포함) → 1·6, `unit-of-work*` → 1·7. ✓
- `construction/` 없음 → 2·1~2·6 전부 회색. `operations/` 없음 → 3·1 회색.
- `adr/ADR.md`, `review/phase1-*.md` → 기타. ✓

## 렌더링

`renderFilePane()`이 **AI-DLC 모드 且 정렬=단계순**이면 `renderAidlcGrouped(files)`를,
아니면 기존 `renderFiles(state.aidlc.files)`를 호출한다.

`renderAidlcGrouped(files)`:
1. 각 파일을 `aidlcClassify(f.name)`로 분류, `phaseNum`/`stepNum`별 버킷에 적재.
2. **정식 스텝 13개를 항상** 순서대로 출력(문서 0개여도). 페이즈가 바뀌면 페이즈 헤더 삽입.
   - 페이즈 헤더: 이모지 + 라벨, 페이즈 색 좌측 보더/배경 틴트(🔵 `--accent`계, 🟢 녹색,
     🟡 노랑).
   - 스텝 헤더: `1·3 Requirements Analysis` + 개수 배지. 문서 0개면 `aidlc-step-empty`
     클래스(회색 dim) + "(아직 없음)".
3. 스텝 하위 문서: **기존 파일 버튼 마크업 재사용**(`renderFiles`의 버튼 생성 로직을
   작은 헬퍼 `makeFileButton(entry)`로 추출해 공유). 클릭→`selectFile`, 최근수정 배지/
   플래그 유지. 문서명은 `f.name`(상대경로)에서 스텝 폴더 접두 제거한 나머지(가독성).
4. 2·0 공통(존재 시), "기타"(존재 시)를 Construction/끝에 배치.
5. 빈 스텝/페이즈도 렌더해 다이어그램형 전체 개요 제공.

## 정렬 옵션 UI

- AI-DLC 모드가 **활성일 때만** 파일 목록 상단에 세그먼트 컨트롤 표시:
  `[단계순] [최근수]`. 기존 `.search-sort`/`.search-sort-btn` 스타일 재사용.
- 선택값 `state.aidlcSort`(`"stage"` | `"recent"`), `localStorage["mdviewer.aidlcSort"]`에
  저장. 기본 `"stage"`.
- 토글 시 `renderFilePane()` 재호출. AI-DLC 모드가 꺼지면 컨트롤 숨김.

## i18n

신규 라벨 EN/KO 추가(목록 동기): `aidlcSortStage`(단계순/By stage),
`aidlcSortRecent`(최근수/Recent), `aidlcStepEmpty`((아직 없음)/(none yet)),
`aidlcOther`(기타/Other). 페이즈/스텝 영문 라벨은 워크플로 고유명사라 비번역(영문 유지).

## 에러 / 엣지

| 상황 | 처리 |
|------|------|
| 분류 안 되는 문서 | "기타" 그룹(숨기지 않음) |
| application-design 폴더 1·6/1·7 혼재 | 파일명으로 구분 |
| construction/plans 계획 문서 | 파일명 접미사로 스텝 매핑 |
| 루트 비표준 파일 | 1·1 Workspace Detection |
| AI-DLC 모드 OFF | 컨트롤 숨김, 기존 동작 |

## 테스트

- 백엔드 무변경 → 기존 Go 테스트 그대로 통과.
- `aidlcClassify`는 순수 함수 → 브라우저에서 실제 `son-local-env/aidlc-docs` 경로 집합으로
  분류 결과 검증(1·1~1·7 매핑, adr/review→기타, 2·x/3·x 회색).
- 단계순/최근수 토글, 회색 빈 스텝 표시, 문서 클릭→열기 브라우저 검증.

## 비목표

- 문서 단위 placeholder(조건부·여분 문서로 부정확). 단계 순서 커스터마이즈.
  접기/펼치기. Operations 세부 스텝(카논 미정).
