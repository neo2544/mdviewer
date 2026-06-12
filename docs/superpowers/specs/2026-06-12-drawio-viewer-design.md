# .drawio 읽기 전용 뷰어 지원 — 설계

날짜: 2026-06-12
상태: 승인됨 (사용자 "진행")

## 목표

MD_Viewer 웹 모드에서 `.drawio`(및 `.dio`) 파일을 열면 draw.io 다이어그램을
읽기 전용으로 렌더링해 보여준다. 편집은 지원하지 않는다 (CSV 뷰어와 동일한 정책).

## 접근 방식

채택: **샌드박스 iframe + draw.io GraphViewer** (기존 `html` kind 패턴 재사용)

- 무거운 viewer 스크립트(~2MB)와 mxGraph 전역 CSS를 iframe 안에 격리해
  메인 SPA(검색 하이라이트, 줌 툴바 등)와의 충돌을 차단한다.
- 파일 변경 감지 시 iframe src 갱신만으로 새로고침된다.

기각: (A) 메인 페이지에 GraphViewer 직접 임베드 — 전역 오염 위험,
(C) embed.diagrams.net 전체 UI — 읽기 전용 목적에 과함.

## 백엔드 (web.go)

- `handleFile` switch에 `case ".drawio", ".dio":` 추가
  → `Kind: "drawio"`, `RawURL: "/api/drawio?path=..."`.
- 새 핸들러 `handleDrawio` (`GET /api/drawio?path=...`):
  - 확장자 검증(.drawio/.dio만 허용) 후 파일 XML을 읽는다.
  - GraphViewer 규격 래퍼 HTML을 반환:
    `<div class="mxgraph" data-mxgraph="{JSON}">` + viewer-static.min.js
    (CDN: viewer.diagrams.net — 기존 marked/hljs/KaTeX CDN 패턴과 동일 정책).
  - data-mxgraph JSON에 XML을 넣을 때 JSON 인코딩 + HTML attribute escape 필수.
  - 뷰어 옵션: 읽기 전용, 다중 페이지 탭, 줌/팬/레이어 툴바, lightbox.
- `canEditKind`/편집 허용 목록에는 추가하지 않는다 (읽기 전용).

## 프런트엔드 (web.go 내 임베드 JS)

- `renderPreview`에 `kind === "drawio"` 분기: html kind와 동일한 샌드박스
  iframe(`allow-scripts allow-popups ...`) 생성, src는 `data.raw_url + "&t=..."`.
- kind 칩 색상 추가: `.chip[data-kind="drawio"]`.
- 최근 파일/팔레트는 기존 kind 전달 구조로 자동 동작.

## 검색 연동

이번 범위에서 제외. .drawio XML은 스타일 문자열 비중이 커서 본문 검색 시
노이즈 매치가 많다. 파일명 검색·트리 탐색은 기존대로 동작.
라벨 텍스트만 추출하는 검색은 필요 시 후속 작업으로 분리.

## 테스트

- Go: `handleFile`이 .drawio에 `kind=drawio`를 반환하는지,
  `/api/drawio`가 XML을 escape해 포함하고 비-drawio 경로를 거부하는지.
- 수동: 빌드 후 Chrome으로 실제 파일
  (`SON_AWS/.../repository-data-flow.drawio`) 렌더링 실측 검증.

## 제약 (메모리 준수)

- web.go 임베드 문자열 안에 백틱 금지.
- 확장자 중복 목록(미리보기/편집/구문강조) 중 미리보기 목록에만 추가.
