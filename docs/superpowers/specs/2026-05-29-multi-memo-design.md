# 다중 메모 관리 설계

작성일: 2026-05-29

## 배경

현재 우측 패널에는 단일 메모 textarea 하나(`#memoArea`)만 있고, `localStorage`의
`mdviewer.memo` 키에만 저장된다. 서버 연동·다중 메모·세션 간 영속성이 없다.

이를 다중 메모 노트북으로 확장한다.

## 결정 사항 (사용자 승인)

- **범위**: 전역 노트북 — 모든 파일에서 동일한 메모 리스트를 공유 (파일에 묶이지 않음)
- **저장 위치**: 신규 JSON 파일 `.mdviewer_memos.json` (favorites/aliases와 동일하게 appRoot)
- **기존 메모**: 최초 로드 시 `localStorage["mdviewer.memo"]`에 내용이 있으면 새 메모 1개로 마이그레이션 후 기존 키 제거
- **리스트 표시명**: 사용자 제목 입력 우선, 없으면 본문 첫 비어있지 않은 줄, 둘 다 없으면 `Untitled`
- **충돌 처리**: 마지막 쓰기 우선 — id 기준 병합, `updatedAt`이 더 최신인 쪽 유지

## 데이터 모델

`.mdviewer_memos.json` = 메모 객체 배열.

```json
[
  {
    "id": "m_<timestamp>_<rand>",
    "title": "사용자 입력 제목(선택, 빈 문자열 가능)",
    "body": "메모 본문",
    "createdAt": "2026-05-29T01:23:45Z",
    "updatedAt": "2026-05-29T01:30:00Z"
  }
]
```

- `id`: 클라이언트가 생성하는 고유 식별자
- `createdAt` / `updatedAt`: RFC3339 (UTC). `updatedAt`은 본문/제목 변경 시 갱신
- 표시명 = `title` 비어있지 않으면 title, 아니면 body 첫 비어있지 않은 줄, 아니면 `Untitled`

## 서버 (web.go) — favorites 패턴 그대로

- `const memosFileName = ".mdviewer_memos.json"` (main.go의 상수 블록)
- `memosPath()`, `loadMemos() []memo`, `saveMemos([]memo) error`
- 핸들러 (`setupRoutes` / mux에 등록):
  - `GET /api/memos` → 전체 메모 반환, `updatedAt` 내림차순
  - `POST /api/memos/save` → `{ "memos": [...] }` 배치 **upsert**.
    - id가 이미 있으면 incoming의 `updatedAt`이 더 최신일 때만 교체 (last-write-wins)
    - id가 없으면 추가
    - **삭제는 하지 않음** → 다른 탭이 만든 메모를 전체 동기화가 덮어쓰지 않음
    - 병합된 전체 집합 반환
  - `POST /api/memos/delete` → `{ "id": "..." }` 즉시 삭제, 남은 집합 반환
- 응답은 기존 `writeJSON` 사용. POST가 아니면 405.

## 동기화 흐름 (클라이언트 JS, web.go 인라인)

상태: 메모 배열을 메모리에 보관 + `localStorage["mdviewer.memos"]`에 미러링.

1. **세션 시작**
   - localStorage 캐시로 즉시 렌더 (오프라인/즉시성)
   - 마이그레이션: 캐시가 비어있고 `mdviewer.memo`(구) 키에 내용이 있으면 메모 1개로 변환
   - `GET /api/memos` → 서버본과 id/`updatedAt` 기준 병합
   - 로컬이 더 최신인 메모는 `/api/memos/save`로 push
   - 최종 병합 결과를 렌더 + localStorage 기록
2. **편집 시**: 활성 메모의 `body`/`title` 갱신 → `updatedAt` = now, dirty 셋에 id 추가, localStorage 즉시 기록, 디바운스(800ms) 후 dirty 메모만 `/api/memos/save` 전송 → 성공 시 dirty 비움
3. **주기적 플러시**: 15초 타이머 — dirty가 있으면 안전망으로 동기화. `beforeunload`에서도 best-effort flush (`navigator.sendBeacon` 또는 동기 저장)
4. **새 메모**: `+` 버튼 → 빈 메모 객체 생성(id/시간 부여), 리스트 맨 위 활성화, 제목 입력 포커스, 즉시 dirty
5. **삭제**: `/api/memos/delete` 호출 → 로컬에서 제거 → 인접 메모 활성화

## UI (우측 패널 `.memo-section` 확장)

- 헤더: `Memo` 제목 + `+` 새 메모 버튼 (기존 actions 영역)
- **메모 리스트**: 스크롤되는 행 목록. 각 행 = 표시명 + 상대 수정시간 + 삭제 `×`. 활성 메모 하이라이트
- **에디터**: 활성 메모의 제목 입력칸(placeholder "제목(선택)") + 본문 textarea(`#memoArea` 재사용)
- 기존 `📋 Copy`(파일명 헤더 포함 복사) 유지 — 활성 메모 본문 대상
- 기존 `Clear` → 활성 메모 본문 비우기 (메모 자체는 유지)
- 메모가 하나도 없을 때: 빈 상태 안내 + `+`로 생성 유도

## 테스트

`search_test.go`가 있는 패키지에 Go 테스트 추가:
- `loadMemos`/`saveMemos` 라운드트립 (임시 디렉터리)
- upsert 병합: 같은 id에 더 최신 `updatedAt`이면 교체, 오래된 것이면 무시, 새 id는 추가
- delete: id 제거 동작

## 비목표 (YAGNI)

- 원격 클라우드 동기화 (여기서 서버 = 로컬 Go 웹서버)
- 메모 폴더/태그/검색 (추후)
- 실시간 멀티탭 푸시(웹소켓) — 폴링/병합으로 충분
