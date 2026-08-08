"use strict";

/**
 * 取得対象一覧の行要素を表示順に取得します。
 *
 * 機能:
 *   - tbody直下にあるdata-source-id付きの行だけを抽出する
 *   - 現在のDOM順を維持した配列へ変換する
 *
 * @param {HTMLTableSectionElement} list - 取得対象一覧のtbody要素。
 * @returns {HTMLTableRowElement[]} 現在の表示順に並んだ取得対象行。
 */
function getSourceOrderRows(list) {
  return Array.from(list.children).filter(
    (child) => child instanceof HTMLTableRowElement && child.hasAttribute("data-source-id"),
  );
}

// ----------------------------------------

/**
 * 取得対象一覧から現在のID順を取得します。
 *
 * 機能:
 *   - data-source-idをDOMの並び通りに読み込む
 *   - 保存要求と並べ替え前後の比較に使用できる文字列配列を返す
 *
 * @param {HTMLTableSectionElement} list - 取得対象一覧のtbody要素。
 * @returns {string[]} 現在の表示順に並んだ取得対象ID。
 */
function getSourceOrderIDs(list) {
  return getSourceOrderRows(list).map((row) => row.dataset.sourceId?.trim() ?? "");
}

// ----------------------------------------

/**
 * 2つの取得対象ID順が一致するかを判定します。
 *
 * 機能:
 *   - 件数と各位置のIDを比較する
 *   - 保存が必要かを判定する
 *
 * @param {string[]} firstOrder - 比較元の取得対象ID順。
 * @param {string[]} secondOrder - 比較先の取得対象ID順。
 * @returns {boolean} ID順が完全に一致する場合はtrue、それ以外はfalse。
 */
function sourceOrdersEqual(firstOrder, secondOrder) {
  return (
    firstOrder.length === secondOrder.length &&
    firstOrder.every((sourceID, index) => sourceID === secondOrder[index])
  );
}

// ----------------------------------------

/**
 * 取得対象行から通知に使用する表示名を取得します。
 *
 * 機能:
 *   - 名前欄のstrong要素から安全なテキストを読み込む
 *   - 名前を取得できない場合は共通名称へフォールバックする
 *
 * @param {HTMLTableRowElement} row - 表示名を持つ取得対象行。
 * @returns {string} 通知へ表示する取得対象名。
 */
function getSourceOrderName(row) {
  const name = row.querySelector("strong")?.textContent?.trim() ?? "";
  return name === "" ? "取得対象" : name;
}

// ----------------------------------------

/**
 * 並べ替え操作の状態を支援技術と画面へ通知します。
 *
 * 機能:
 *   - aria-live領域の文言を更新する
 *   - 成功または失敗に対応する表示色を切り替える
 *
 * @param {HTMLElement} status - 並べ替え状態を表示するaria-live要素。
 * @param {string} message - 利用者へ通知する文言。
 * @param {"default"|"success"|"error"} tone - 通知の表示種別。
 * @returns {void} 返り値はありません。
 */
function announceSourceOrder(status, message, tone = "default") {
  status.textContent = message;
  status.classList.toggle("is-success", tone === "success");
  status.classList.toggle("is-error", tone === "error");
}

// ----------------------------------------

/**
 * 並べ替え保存中の操作可否を切り替えます。
 *
 * 機能:
 *   - 保存中フラグとaria-busyを同期する
 *   - すべてのドラッグハンドルを一時的に無効化して競合操作を防ぐ
 *
 * @param {object} state - 取得対象一覧の操作状態。
 * @param {boolean} isSaving - 保存中にする場合はtrue、解除する場合はfalse。
 * @returns {void} 返り値はありません。
 */
function setSourceOrderSaving(state, isSaving) {
  state.saving = isSaving;
  state.list.classList.toggle("is-saving", isSaving);
  state.list.setAttribute("aria-busy", String(isSaving));
  state.list.querySelectorAll(".source-drag-handle").forEach((handle) => {
    if (handle instanceof HTMLButtonElement) {
      handle.disabled = isSaving;
    }
  });
}

// ----------------------------------------

/**
 * 取得対象一覧を指定されたID順へ復元します。
 *
 * 機能:
 *   - 保存失敗時に操作開始前のDOM順へ戻す
 *   - 指定順に含まれない行がある場合は末尾に維持する
 *
 * @param {HTMLTableSectionElement} list - 復元対象のtbody要素。
 * @param {string[]} sourceOrder - 復元する取得対象ID順。
 * @returns {void} 返り値はありません。
 */
function restoreSourceOrder(list, sourceOrder) {
  const rows = getSourceOrderRows(list);
  const rowsByID = new Map(rows.map((row) => [row.dataset.sourceId?.trim() ?? "", row]));
  const restoredIDs = new Set();

  sourceOrder.forEach((sourceID) => {
    const row = rowsByID.get(sourceID);
    if (row !== undefined) {
      list.append(row);
      restoredIDs.add(sourceID);
    }
  });
  rows.forEach((row) => {
    const sourceID = row.dataset.sourceId?.trim() ?? "";
    if (!restoredIDs.has(sourceID)) {
      list.append(row);
    }
  });
}

// ----------------------------------------

/**
 * 指定した取得対象IDのドラッグハンドルへフォーカスを戻します。
 *
 * 機能:
 *   - 保存後もキーボード操作の現在位置を維持する
 *   - 対象行が見つからない場合は何もしない
 *
 * @param {HTMLTableSectionElement} list - 取得対象一覧のtbody要素。
 * @param {string} sourceID - フォーカスを戻す取得対象ID。
 * @returns {void} 返り値はありません。
 */
function focusSourceOrderHandle(list, sourceID) {
  const row = getSourceOrderRows(list).find(
    (candidate) => (candidate.dataset.sourceId?.trim() ?? "") === sourceID,
  );
  const handle = row?.querySelector(".source-drag-handle");
  if (handle instanceof HTMLButtonElement) {
    handle.focus({ preventScroll: true });
  }
}

// ----------------------------------------

/**
 * 現在の取得対象ID順を管理画面へ送信します。
 *
 * 機能:
 *   - CSRFトークンと反復source_idsをURLエンコードする
 *   - 管理画面の順序更新エンドポイントへ同一オリジンPOSTを送る
 *   - HTTPエラー時はサーバーの安全な案内文を保存失敗として呼び出し元へ伝える
 *
 * @param {HTMLInputElement} csrfInput - CSRFトークンを保持するhidden入力。
 * @param {string[]} sourceOrder - 保存する取得対象ID順。
 * @returns {Promise<void>} 保存成功時に解決するPromise。
 */
async function postSourceOrder(csrfInput, sourceOrder) {
  const parameters = new URLSearchParams();
  parameters.set("_csrf", csrfInput.value);
  sourceOrder.forEach((sourceID) => {
    if (!/^[1-9][0-9]*$/.test(sourceID)) {
      throw new Error("取得対象IDが正しくありません。");
    }
    parameters.append("source_ids", sourceID);
  });

  const response = await window.fetch("/admin/sources/order", {
    method: "POST",
    credentials: "same-origin",
    headers: {
      Accept: "application/json",
      "Content-Type": "application/x-www-form-urlencoded; charset=UTF-8",
    },
    body: parameters.toString(),
  });
  if (!response.ok) {
    let message = "表示順を保存できませんでした。";
    try {
      const data = await response.json();
      if (
        typeof data === "object" &&
        data !== null &&
        typeof data.error === "string" &&
        data.error.trim() !== ""
      ) {
        message = data.error.trim();
      }
    } catch (_error) {
      // JSON以外のエラー応答では共通メッセージを使用します。
    }
    throw new Error(message);
  }
}

// ----------------------------------------

/**
 * 取得対象の表示順を保存し、結果に応じて画面状態を確定します。
 *
 * 機能:
 *   - 保存中の追加操作を防ぎながら現在順をPOSTする
 *   - 成功時は完了を通知する
 *   - 失敗時は操作前の順番へ戻してエラーを通知する
 *
 * @param {object} state - 取得対象一覧の操作状態。
 * @param {string[]} previousOrder - 保存失敗時に復元する操作前のID順。
 * @param {string} focusSourceID - 保存後にフォーカスを戻す取得対象ID。
 * @returns {Promise<boolean>} 保存成功時はtrue、失敗時はfalse。
 */
async function saveSourceOrder(state, previousOrder, focusSourceID) {
  if (state.saving) {
    return false;
  }

  const sourceOrder = getSourceOrderIDs(state.list);
  if (sourceOrdersEqual(previousOrder, sourceOrder)) {
    return true;
  }

  const movedRow = getSourceOrderRows(state.list).find(
    (row) => (row.dataset.sourceId?.trim() ?? "") === focusSourceID,
  );
  const movedPosition = sourceOrder.indexOf(focusSourceID) + 1;
  const movementDescription =
    movedRow instanceof HTMLTableRowElement && movedPosition > 0
      ? `${getSourceOrderName(movedRow)}を${movedPosition}番目へ移動`
      : "表示順を変更";

  setSourceOrderSaving(state, true);
  announceSourceOrder(
    state.status,
    `${movementDescription}しました。表示順を保存しています。`,
  );
  try {
    await postSourceOrder(state.csrfInput, sourceOrder);
    announceSourceOrder(
      state.status,
      `${movementDescription}し、表示順を保存しました。`,
      "success",
    );
    return true;
  } catch (error) {
    restoreSourceOrder(state.list, previousOrder);
    const message =
      error instanceof Error && error.message.trim() !== ""
        ? error.message.trim()
        : "表示順を保存できませんでした。";
    announceSourceOrder(
      state.status,
      `${message} 元の順番に戻しました。`,
      "error",
    );
    return false;
  } finally {
    setSourceOrderSaving(state, false);
    focusSourceOrderHandle(state.list, focusSourceID);
  }
}

// ----------------------------------------

/**
 * キーボード操作で取得対象行を指定位置へ移動します。
 *
 * 機能:
 *   - 上下矢印で1行、HomeとEndで先頭または末尾へ移動する
 *   - 移動直後に表示順を保存する
 *   - 保存中またはポインタードラッグ中の競合操作を拒否する
 *
 * @param {KeyboardEvent} event - ドラッグハンドルで発生したキーボードイベント。
 * @param {object} state - 取得対象一覧の操作状態。
 * @returns {void} 返り値はありません。
 */
function moveSourceOrderByKeyboard(event, state) {
  if (!["ArrowUp", "ArrowDown", "Home", "End"].includes(event.key)) {
    return;
  }

  const target = event.target;
  if (!(target instanceof HTMLButtonElement) || !target.matches(".source-drag-handle")) {
    return;
  }
  event.preventDefault();
  if (state.saving || state.draggedRow !== null) {
    announceSourceOrder(state.status, "表示順の保存が完了するまでお待ちください。");
    return;
  }

  const row = target.closest("tr[data-source-id]");
  if (!(row instanceof HTMLTableRowElement)) {
    return;
  }
  const rows = getSourceOrderRows(state.list);
  const currentIndex = rows.indexOf(row);
  if (currentIndex < 0) {
    return;
  }

  let destinationIndex = currentIndex;
  switch (event.key) {
    case "ArrowUp":
      destinationIndex = Math.max(0, currentIndex - 1);
      break;
    case "ArrowDown":
      destinationIndex = Math.min(rows.length - 1, currentIndex + 1);
      break;
    case "Home":
      destinationIndex = 0;
      break;
    case "End":
      destinationIndex = rows.length - 1;
      break;
    default:
      return;
  }

  const sourceName = getSourceOrderName(row);
  if (destinationIndex === currentIndex) {
    const edge = currentIndex === 0 ? "先頭" : "末尾";
    announceSourceOrder(state.status, `${sourceName}はすでに${edge}です。`);
    return;
  }

  const previousOrder = getSourceOrderIDs(state.list);
  const referenceRow = rows[destinationIndex];
  if (destinationIndex < currentIndex) {
    state.list.insertBefore(row, referenceRow);
  } else {
    state.list.insertBefore(row, referenceRow.nextSibling);
  }
  target.focus({ preventScroll: true });

  const sourceID = row.dataset.sourceId?.trim() ?? "";
  void saveSourceOrder(state, previousOrder, sourceID);
}

// ----------------------------------------

/**
 * ポインターによる取得対象行のドラッグを開始します。
 *
 * 機能:
 *   - ドラッグハンドルから開始した主ボタン操作だけを受け付ける
 *   - 操作前のID順と対象行を保持する
 *   - ポインターキャプチャで一覧外へ移動しても操作を継続する
 *
 * @param {PointerEvent} event - ドラッグ開始のポインターイベント。
 * @param {object} state - 取得対象一覧の操作状態。
 * @returns {void} 返り値はありません。
 */
function startSourceOrderDrag(event, state) {
  const target = event.target;
  const handle = target instanceof Element ? target.closest(".source-drag-handle") : null;
  if (!(handle instanceof HTMLButtonElement)) {
    return;
  }
  if (event.pointerType === "mouse" && event.button !== 0) {
    return;
  }
  if (state.saving || state.draggedRow !== null) {
    event.preventDefault();
    announceSourceOrder(state.status, "表示順の保存が完了するまでお待ちください。");
    return;
  }

  const row = handle.closest("tr[data-source-id]");
  if (!(row instanceof HTMLTableRowElement)) {
    return;
  }

  event.preventDefault();
  handle.focus({ preventScroll: true });
  state.activePointerID = event.pointerId;
  state.draggedRow = row;
  state.originalOrder = getSourceOrderIDs(state.list);
  row.classList.add("is-dragging");
  state.list.classList.add("is-reordering");
  document.body.classList.add("is-source-order-dragging");
  state.list.setPointerCapture(event.pointerId);

  announceSourceOrder(
    state.status,
    `${getSourceOrderName(row)}を移動しています。目的の位置で離してください。`,
  );
}

// ----------------------------------------

/**
 * ポインターの縦位置に合わせてドラッグ中の取得対象行を配置します。
 *
 * 機能:
 *   - ドラッグ対象以外の各行の中央を挿入位置の境界にする
 *   - すべての行より下にある場合は一覧末尾へ配置する
 *
 * @param {object} state - 取得対象一覧の操作状態。
 * @param {number} clientY - ビューポート内のポインターY座標。
 * @returns {void} 返り値はありません。
 */
function placeSourceOrderDraggedRow(state, clientY) {
  if (!(state.draggedRow instanceof HTMLTableRowElement)) {
    return;
  }

  const otherRows = getSourceOrderRows(state.list).filter((row) => row !== state.draggedRow);
  const referenceRow = otherRows.find((row) => {
    const bounds = row.getBoundingClientRect();
    return clientY < bounds.top + bounds.height / 2;
  });
  if (referenceRow === undefined) {
    state.list.append(state.draggedRow);
  } else {
    state.list.insertBefore(state.draggedRow, referenceRow);
  }
}

// ----------------------------------------

/**
 * ドラッグ中の画面端スクロールを1フレーム実行します。
 *
 * 機能:
 *   - 指定方向へ画面を少量スクロールする
 *   - スクロール後の行位置でドラッグ対象の挿入先を再計算する
 *   - ドラッグ中かつ端にある間は次フレームを予約する
 *
 * @param {object} state - 取得対象一覧の操作状態。
 * @returns {void} 返り値はありません。
 */
function runSourceOrderAutoScroll(state) {
  state.autoScrollFrameID = null;
  if (
    state.draggedRow === null ||
    state.autoScrollDirection === 0 ||
    state.lastPointerClientY === null
  ) {
    return;
  }

  window.scrollBy({ top: state.autoScrollDirection * 12, behavior: "auto" });
  placeSourceOrderDraggedRow(state, state.lastPointerClientY);
  state.autoScrollFrameID = window.requestAnimationFrame(() => {
    runSourceOrderAutoScroll(state);
  });
}

// ----------------------------------------

/**
 * ポインター位置からドラッグ中の画面端スクロール方向を更新します。
 *
 * 機能:
 *   - 上端と下端の範囲では継続スクロールを開始する
 *   - 中央範囲へ戻った場合は予約済みスクロールを停止する
 *
 * @param {object} state - 取得対象一覧の操作状態。
 * @param {number} clientY - ビューポート内のポインターY座標。
 * @returns {void} 返り値はありません。
 */
function updateSourceOrderAutoScroll(state, clientY) {
  const scrollBoundary = 56;
  let direction = 0;
  if (clientY < scrollBoundary) {
    direction = -1;
  } else if (clientY > window.innerHeight - scrollBoundary) {
    direction = 1;
  }

  state.lastPointerClientY = clientY;
  state.autoScrollDirection = direction;
  if (direction !== 0 && state.autoScrollFrameID === null) {
    state.autoScrollFrameID = window.requestAnimationFrame(() => {
      runSourceOrderAutoScroll(state);
    });
    return;
  }
  if (direction === 0 && state.autoScrollFrameID !== null) {
    window.cancelAnimationFrame(state.autoScrollFrameID);
    state.autoScrollFrameID = null;
  }
}

// ----------------------------------------

/**
 * ドラッグ中の画面端スクロールを停止します。
 *
 * 機能:
 *   - 予約済みフレームを取り消す
 *   - スクロール方向と最後のポインター位置を初期化する
 *
 * @param {object} state - 取得対象一覧の操作状態。
 * @returns {void} 返り値はありません。
 */
function stopSourceOrderAutoScroll(state) {
  if (state.autoScrollFrameID !== null) {
    window.cancelAnimationFrame(state.autoScrollFrameID);
  }
  state.autoScrollFrameID = null;
  state.autoScrollDirection = 0;
  state.lastPointerClientY = null;
}

// ----------------------------------------

/**
 * ポインター位置に合わせて取得対象行を上下へ移動します。
 *
 * 機能:
 *   - 各行の縦方向中央を境界として挿入位置を決定する
 *   - 画面上端または下端付近では継続スクロールを開始する
 *
 * @param {PointerEvent} event - ドラッグ中のポインターイベント。
 * @param {object} state - 取得対象一覧の操作状態。
 * @returns {void} 返り値はありません。
 */
function moveSourceOrderDrag(event, state) {
  if (
    state.draggedRow === null ||
    state.activePointerID === null ||
    event.pointerId !== state.activePointerID
  ) {
    return;
  }
  event.preventDefault();
  placeSourceOrderDraggedRow(state, event.clientY);
  updateSourceOrderAutoScroll(state, event.clientY);
}

// ----------------------------------------

/**
 * ポインタードラッグの一時状態と見た目を解除します。
 *
 * 機能:
 *   - DOM移動の影響を受けない一覧要素のポインターキャプチャを安全に解放する
 *   - 行、一覧、bodyのドラッグ状態クラスを除去する
 *
 * @param {object} state - 取得対象一覧の操作状態。
 * @returns {void} 返り値はありません。
 */
function clearSourceOrderDrag(state) {
  const pointerID = state.activePointerID;
  const row = state.draggedRow;

  state.activePointerID = null;
  state.draggedRow = null;
  stopSourceOrderAutoScroll(state);
  if (pointerID !== null && state.list.hasPointerCapture(pointerID)) {
    state.list.releasePointerCapture(pointerID);
  }
  row?.classList.remove("is-dragging");
  state.list.classList.remove("is-reordering");
  document.body.classList.remove("is-source-order-dragging");
}

// ----------------------------------------

/**
 * ポインタードラッグを確定して変更後の表示順を保存します。
 *
 * 機能:
 *   - 操作開始時と現在のID順を比較する
 *   - 順序が変わった場合だけ保存処理を開始する
 *
 * @param {PointerEvent} event - ドラッグ終了のポインターイベント。
 * @param {object} state - 取得対象一覧の操作状態。
 * @returns {void} 返り値はありません。
 */
function finishSourceOrderDrag(event, state) {
  if (
    state.draggedRow === null ||
    state.activePointerID === null ||
    event.pointerId !== state.activePointerID
  ) {
    return;
  }
  event.preventDefault();

  const row = state.draggedRow;
  const previousOrder = state.originalOrder.slice();
  const sourceID = row.dataset.sourceId?.trim() ?? "";
  clearSourceOrderDrag(state);
  state.originalOrder = [];

  if (sourceOrdersEqual(previousOrder, getSourceOrderIDs(state.list))) {
    announceSourceOrder(state.status, "表示順は変更されていません。");
    return;
  }
  void saveSourceOrder(state, previousOrder, sourceID);
}

// ----------------------------------------

/**
 * 中断されたポインタードラッグを操作開始前の順番へ戻します。
 *
 * 機能:
 *   - pointercancelまたは予期しないキャプチャ解除を取り消しとして扱う
 *   - 保存要求を送らずに元のDOM順を復元する
 *
 * @param {PointerEvent} event - 中断を通知するポインターイベント。
 * @param {object} state - 取得対象一覧の操作状態。
 * @returns {void} 返り値はありません。
 */
function cancelSourceOrderDrag(event, state) {
  if (
    state.draggedRow === null ||
    state.activePointerID === null ||
    event.pointerId !== state.activePointerID
  ) {
    return;
  }

  const previousOrder = state.originalOrder.slice();
  const sourceID = state.draggedRow.dataset.sourceId?.trim() ?? "";
  clearSourceOrderDrag(state);
  state.originalOrder = [];
  restoreSourceOrder(state.list, previousOrder);
  announceSourceOrder(state.status, "並べ替え操作を取り消しました。");
  focusSourceOrderHandle(state.list, sourceID);
}

// ----------------------------------------

/**
 * 取得対象一覧のポインター操作、キーボード操作、保存処理を初期化します。
 *
 * 機能:
 *   - 必要な一覧、CSRF、aria-live要素を確認する
 *   - Pointer Eventsによる上下ドラッグを登録する
 *   - 上下矢印、Home、Endによるキーボード移動を登録する
 *
 * @returns {void} 返り値はありません。
 */
function initializeSourceOrder() {
  const list = document.querySelector("#source-order-list");
  const csrfInput = document.querySelector("#source-order-csrf");
  const status = document.querySelector("#source-order-status");
  if (
    !(list instanceof HTMLTableSectionElement) ||
    !(csrfInput instanceof HTMLInputElement) ||
    !(status instanceof HTMLElement)
  ) {
    return;
  }

  const state = {
    list,
    csrfInput,
    status,
    activePointerID: null,
    draggedRow: null,
    originalOrder: [],
    saving: false,
    autoScrollFrameID: null,
    autoScrollDirection: 0,
    lastPointerClientY: null,
  };
  setSourceOrderSaving(state, false);

  list.addEventListener("pointerdown", (event) => {
    startSourceOrderDrag(event, state);
  });
  list.addEventListener("pointermove", (event) => {
    moveSourceOrderDrag(event, state);
  });
  list.addEventListener("pointerup", (event) => {
    finishSourceOrderDrag(event, state);
  });
  list.addEventListener("pointercancel", (event) => {
    cancelSourceOrderDrag(event, state);
  });
  list.addEventListener("lostpointercapture", (event) => {
    cancelSourceOrderDrag(event, state);
  });
  list.addEventListener("keydown", (event) => {
    moveSourceOrderByKeyboard(event, state);
  });
}

// ----------------------------------------

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", initializeSourceOrder, { once: true });
} else {
  initializeSourceOrder();
}
