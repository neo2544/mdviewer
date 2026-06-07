# CSV 뷰어 Excel형 개선 — 설계

작성일: 2026-06-07

## 배경 / 목표

CSV 표 뷰어([[mdviewer-csv-viewer]])에서 긴 셀 내용이 줄임표로 잘려 안 보이고, 셀의
위치(행·열)를 알기 어렵다. Excel과 유사하게 다음을 추가한다.

1. **컬럼 드래그 리사이즈** — 컬럼 경계를 끌어 폭 조정 (세션 유지).
2. **수식줄(formula bar)** — 셀 클릭 시 상단 바에 셀 전체 내용 표시 (읽기 전용).
3. **셀 참조 + Excel식 거터** — 좌측 행번호 열 + 상단 열문자(A,B,C) 머리글, 셀 클릭 시
   `A1`식 참조 표시 및 해당 행/열 머리글 강조.

전부 프론트엔드 작업이다. `/api/csv`가 이미 `header/rows/page/page_size/total_rows`를
반환하므로 **백엔드 변경은 없다**.

## 결정 사항 (확정)

| 항목 | 결정 |
|------|------|
| 행/열 머리글 | Excel식 전체 거터(좌측 행번호 + 상단 열문자), 클릭 시 머리글 강조 |
| 행 번호 기준 | 헤더 포함 절대: 헤더=행 1, 첫 데이터=행 2, 페이지 무관 |
| 컬럼 폭 | 드래그 리사이즈 + 수식줄. 긴 셀은 줄임표 유지, 폭은 세션 유지 |
| 헤더 고정 | 스크롤 시 열문자 행 + CSV 헤더 행 모두 sticky (2행 고정) |

## 표 구조 (drawCsv 재구성)

`table-layout: fixed` + `<colgroup>`. 거터 열 1개 + 데이터 열 N개.

```
┌──────┬─────────┬─────────┐  ← 열문자 행 A,B,C…   sticky top:0
│ 코너 │    A    │    B    │     코너 = sticky top+left (z 최상)
├──────┼─────────┼─────────┤  ← CSV 헤더 = 행 1     sticky top:(열문자행 높이)
│  1   │ #EUtran │ acBar…  │     거터 "1"
├──────┼─────────┼─────────┤
│  2   │ Managed…│ 95      │  ← tbody, 거터 = 절대 Excel 행번호
│  3   │ …       │ …       │
```

- **sticky**: 열문자 행 `top:0`; CSV 헤더 행 `top: var(--csv-letter-h)`(열문자 행 고정
  높이, 예 22px); 거터 열(`.csv-rownum`/`.csv-corner`) `left:0`; 코너는 top+left 동시,
  z-index 최상.
- **행번호**: 데이터 행 i(0-based, 페이지 내) → `(page-1)*page_size + i + 2`.
  (헤더=1, 첫 데이터=2.) 헤더 행 거터 = `1`.
- **열문자**: colIndex 0→A, 25→Z, 26→AA … bijective base-26 변환 함수 `colLetter(n)`.

## 컬럼 드래그 리사이즈

- `<col>` 요소에 폭 지정. 각 열문자 `th` 우측에 드래그 핸들(`.csv-resizer`).
- pointer 드래그로 해당 `<col>`의 px 폭 변경 → `csvState.colWidths[colIndex]`에 저장.
- `colWidths`는 **페이지 이동·페이지 크기 변경 후에도 유지**(같은 path 동안). path가
  바뀌면(다른 CSV 열기) 초기화. 기본 폭 상수(예 200px). 거터 폭은 행번호 자릿수에
  맞춘 고정(예 56px).
- 긴 셀: `overflow:hidden; text-overflow:ellipsis; white-space:nowrap`.

## 수식줄 (읽기 전용)

- 컨트롤 바(이전/다음/페이지 크기) 아래, 표 위에 `.csv-formula` 바:
  `[이름박스 .csv-namebox] [내용 .csv-cellview]`.
- 셀(td) 또는 헤더(th) 클릭 시:
  - 이름박스 = `colLetter(col) + excelRow` (예 `A2`).
  - 내용 = 셀 전체 텍스트(개행 포함; `.csv-cellview`는 읽기 전용, `white-space:pre-wrap`
    또는 가로 스크롤).
  - 선택 셀에 `.csv-selected`, 해당 열문자 th와 행번호 거터에 `.csv-head-active` 강조.
- 선택 상태 `csvState.selected = {rowAbs, col}`; 페이지 이동/크기 변경 시 해제하고
  수식줄 비움.

## 상태 / 데이터 흐름

- `csvState`에 추가: `colWidths`(객체, path 동안 지속), `selected`(페이지당).
- `drawCsv(resp)`가 `resp.page`/`resp.page_size`로 행번호 계산, `colWidths` 재적용.
- `renderCsv(data)`(파일 진입)에서 `colWidths={}`, `selected=null` 초기화.

## i18n / 테스트 / 에러

- 새 사용자 텍스트 거의 없음(거터·수식줄은 숫자/기호). 필요 시 이름박스 빈 상태 등
  EN/KO 한두 개만 추가.
- 백엔드 무변경 → 기존 Go 테스트 그대로 통과. 프론트는 브라우저로 검증:
  거터·열문자·행번호 정확성(페이지별 절대값), 2행 sticky(가로/세로 스크롤),
  드래그 리사이즈+페이지 이동 후 폭 유지, 셀 클릭→수식줄/참조/강조, 빈 파일.

## 비목표

- 셀 편집(읽기 전용 유지), 정렬/필터, 범위 선택·다중 셀 복사. 단일 셀 선택만.
