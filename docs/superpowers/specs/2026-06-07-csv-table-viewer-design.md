# CSV/TSV 표 뷰어 + 페이지네이션 + 인덱스 캐싱 — 설계

작성일: 2026-06-07

## 1. 배경 / 목표

현재 `.csv`, `.tsv` 파일은 `handleFile`에서 `text` kind로 분류되어 코드 뷰어
(`renderCodeFile`)로 평문 표시되며, 텍스트 편집기로 수정도 가능하다.

목표:

- CSV/TSV를 **표(table)** 로 렌더링한다.
- 대용량 파일 대비 **백엔드에서 행을 잘라(페이지네이션)** 보낸다.
- 헤더 고정 + 페이지 크기 선택 UI를 제공한다.
- 표 뷰는 **완전 읽기 전용**으로 한다 (CSV/TSV 편집 기능 제거).
- 파일이 변경되지 않았으면 **인덱스 캐시로 전체 재스캔 없이** 페이지를 가져온다.

## 2. 결정 사항 (확정)

| 항목 | 결정 |
|------|------|
| 편집 | 표는 완전 읽기 전용. CSV/TSV 편집 기능 제거 |
| 페이지네이션 | 백엔드 행 슬라이싱 + 전체 행수 반환 |
| 표시 옵션 | 헤더 고정(sticky) + 페이지 크기 선택(50/100/500) |
| 캐싱 | 레코드 오프셋 인덱스 캐시, modTime·size 기반 무효화, LRU 16개 상한 |

## 3. 아키텍처 개요

- CSV/TSV를 기존 `text` kind에서 분리해 새 `csv` kind로 처리한다.
- `handleFile`은 표 데이터를 싣지 않고 메타데이터(kind=`csv`, size, mod_time)만
  반환한다. 프론트가 별도 API로 페이지 단위 데이터를 가져온다.
- 새 엔드포인트 `GET /api/csv?path=&page=&page_size=` 가 페이지 구간 행 + 헤더 +
  전체 행수를 반환한다. 파일별 오프셋 인덱스를 캐시해 변경이 없으면 재스캔하지
  않는다.

데이터 흐름:

```
파일 선택 → /api/file (kind=csv, 메타만)
          → 프론트 renderCsv() → /api/csv?page=1&page_size=100
          → 표 렌더 + 컨트롤바
페이지 이동/크기 변경 → /api/csv?page=N&page_size=M (같은 path)
```

## 4. 백엔드 설계 (web.go)

### 4.1 라우팅

```go
mux.HandleFunc("/api/csv", s.handleCSV)
```

### 4.2 handleFile 변경

- `.csv`, `.tsv` 를 기존 `text` case에서 제거하고 별도 case 추가:

```go
case ".csv", ".tsv":
    resp.Kind = "csv"
    // content 미포함 — 표 데이터는 /api/csv 로 별도 로드
```

### 4.3 편집 비활성화

- `handleSaveFile`의 허용 확장자 목록에서 `.csv`, `.tsv` 제거.
- 프론트 `canEditKind`는 `markdown`/`text`만 허용하므로 `csv` kind는 자동으로 편집
  불가가 된다(추가 변경 불필요).

### 4.4 응답 구조체

```go
type csvResponse struct {
    Path      string     `json:"path"`
    Delimiter string     `json:"delimiter"`   // "," or "\t"
    Header    []string   `json:"header"`
    Rows      [][]string `json:"rows"`
    Page      int        `json:"page"`
    PageSize  int        `json:"page_size"`
    TotalRows int        `json:"total_rows"`  // 헤더 제외 데이터 행수
}
```

### 4.5 handleCSV 동작

1. `path` 검증 → `filepath.Abs` → `os.Stat`(없음/디렉터리 에러 처리).
2. 구분자: `.tsv` → `'\t'`, 그 외 → `','`.
3. 파라미터 검증:
   - `page` ≥ 1 (기본 1, 범위 밖이면 클램프).
   - `page_size` ∈ {50, 100, 500} 화이트리스트 (기본 100, 그 외 값은 100으로).
4. 인덱스 확보 (§4.6).
5. `offset = (page-1) * page_size`. `offset ≥ total`이면 빈 `rows` 반환.
6. `offsets[offset]` 위치로 `Seek` → 페이지 구간만 읽어 `csv.Reader`로 파싱
   (`LazyQuotes=true`, `FieldsPerRecord=-1`).
7. `csvResponse` 작성 후 `writeJSON`.

### 4.6 인덱스 캐시

```go
type csvIndex struct {
    modTime time.Time
    size    int64
    offsets []int64 // 각 데이터 레코드의 시작 바이트 오프셋 (헤더 다음 행부터)
    header  []string
    total   int     // 헤더 제외 데이터 행수
    delim   rune
}

type csvCache struct {
    mu    sync.Mutex
    m     map[string]*csvIndex // key: absPath
    order []string             // LRU 추적용, 최근 사용이 뒤
}
```

- `csvCache`는 `webServer`에 필드로 보유하고 서버 생성 시 초기화.
- **조회**: `os.Stat`의 `modTime`+`size`가 캐시 항목과 같으면 재사용.
- **무효화/재구축**: 캐시 미스 또는 modTime·size 불일치 시 1회 풀 패스로 인덱스
  재구축 후 저장. LRU가 16개를 초과하면 가장 오래된 항목 제거.

### 4.7 오프셋 인덱스 구축 (따옴표 인식 스캐너)

`encoding/csv`는 레코드별 바이트 오프셋을 노출하지 않으므로 따옴표 인식 바이트
스캐너를 직접 구현한다.

규칙:

- 파일을 바이트 순회하며 `inQuote` 상태 유지. `"` 를 만나면 토글.
- `inQuote == false` 일 때의 `\n`(직전 `\r` 포함)을 **레코드 경계**로 인식.
- 각 레코드의 시작 바이트 오프셋을 기록한다.
  - 첫 레코드(오프셋 0) = 헤더. 헤더는 따로 파싱해 `index.header`에 저장.
  - 나머지 레코드 시작 오프셋을 `offsets`에 순서대로 저장 → `total = len(offsets)`.
- 마지막 레코드에 trailing newline이 없어도 EOF를 경계로 처리.
- 빈 파일/헤더만 있는 파일: `offsets`는 빈 슬라이스, `total = 0`.

**안전장치 (폴백)**: 페이지 구간을 `csv.Reader`로 파싱하다 에러가 나면(드문
LazyQuotes 경계), 해당 캐시 인덱스를 폐기하고 파일 처음부터 `csv.Reader`로
순차 파싱하며 offset만큼 건너뛰는 방식으로 폴백한다. 잘못된 표시를 방지한다.

## 5. 프론트엔드 설계 (web.go 임베드)

> 메모리 주의: 프론트엔드 전체가 `web.go` 단일 파일에 임베드되어 있고 백틱 금지,
> 목록 중복 3곳 규칙이 있다. i18n은 `i18nDict()`/`t()`/`data-i18n` 구조를 따른다.

### 5.1 renderPreview 분기

```js
if (data.kind === "csv") { await renderCsv(data); return; }
```
(기존 `text` 분기 위/근처에 추가)

### 5.2 renderCsv(data)

- 상태: 현재 path, page, pageSize 보관.
- `/api/csv?path=&page=&page_size=` fetch → JSON.
- 컨테이너 구성:
  - **컨트롤 바**: 이전/다음 버튼, "X / Y 페이지" 표시, 총 행수 표시,
    페이지 크기 선택(50/100/500).
  - **표**: `<table>` + `<thead>`(헤더, sticky) + `<tbody>`(행). 빈 셀 안전 처리.
- 페이지 이동/크기 변경 시 같은 path로 재요청 후 표/컨트롤 갱신.
- 첫 페이지면 이전 버튼 비활성, 마지막 페이지면 다음 버튼 비활성.

### 5.3 스타일

- `thead th { position: sticky; top: 0; }` 로 헤더 고정.
- 긴 셀: `max-width` + `text-overflow: ellipsis` 또는 표 가로 스크롤.
- chip / recent-file 의 kind 표기에 `csv` 추가 (시각적 구분 색).

### 5.4 i18n

EN/KO 라벨 추가 (목록 중복 3곳 규칙 준수):
- 이전 / Previous, 다음 / Next, 페이지 / Page, 행 / rows, 페이지 크기 / Page size.

## 6. 에러 처리

| 상황 | 처리 |
|------|------|
| path 누락 / 디렉터리 / 없음 | 기존 패턴대로 HTTP 4xx |
| 읽기 실패 | HTTP 500 |
| 파싱 실패 (페이지 구간) | 인덱스 폐기 후 풀 재파싱 폴백 |
| 폴백도 실패 | 에러 JSON → 프론트 "표로 표시할 수 없습니다" 폴백 메시지 |
| 빈 파일 / 헤더만 | header(있으면) + 빈 rows + total 0 정상 응답 |

## 7. 테스트 (TDD) — csv_test.go

오프셋 스캐너 + handleCSV 페이지네이션을 테스트한다.

- 오프셋 스캐너: 단순 행, 따옴표 안 쉼표, 따옴표 안 개행, `\r\n` 줄바꿈,
  trailing newline 유무, 빈 파일, 헤더만 있는 파일.
- handleCSV: 첫/중간/마지막 페이지 경계, 전체 행수 정확성, 탭 구분자(tsv),
  page_size 화이트리스트(잘못된 값 → 100), 범위 밖 page 클램프.
- 캐시: 동일 modTime·size 재요청 시 인덱스 재사용(재구축 안 함), 파일 변경 후
  재구축. (검증 가능한 형태로 — 예: 재구축 횟수 카운터 또는 캐시 항목 확인)

## 8. 트레이드오프 / 비목표

- 정렬/검색 필터는 이번 범위 제외 (페이지네이션과 결합 시 전체 스캔 필요).
- 오프셋 인덱스는 전체 저장(행당 8바이트). 극단적 대용량용 sparse 인덱스는
  필요해지면 추가(YAGNI).
- 인코딩은 UTF-8 가정 (BOM은 헤더 첫 셀에서 흡수 처리 권장).
- 표 뷰는 읽기 전용 — 편집이 필요하면 추후 별도 설계.
