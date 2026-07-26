/**
 * 取得元種別に応じて固有設定欄の表示と送信可否を切り替えます。
 *
 * 機能:
 *   - 選択中のコネクタと一致する設定欄だけを表示する
 *   - 非表示欄の入力要素を無効化して別コネクタの値を送信しない
 *
 * @param {HTMLFormElement} form - 取得対象の作成・編集フォーム。
 * @returns {void} 返り値はありません。
 */
function updateConnectorFields(form) {
  const kindSelect = form.querySelector("#source-kind");
  if (!(kindSelect instanceof HTMLSelectElement)) {
    return;
  }

  const selectedKind = kindSelect.value;
  form.querySelectorAll("[data-connector-fields]").forEach((section) => {
    const isActive = section.dataset.connectorFields === selectedKind;
    section.hidden = !isActive;
    section.querySelectorAll("input, select, textarea, button").forEach((control) => {
      control.disabled = !isActive;
    });
  });
}

// ----------------------------------------

/**
 * 取得対象フォームのコネクタ切替処理を初期化します。
 *
 * 機能:
 *   - 対象フォームが存在する画面だけで初期表示を整える
 *   - コネクタ選択変更のたびに固有設定欄を更新する
 *
 * @returns {void} 返り値はありません。
 */
function initializeSourceForm() {
  const form = document.querySelector("#source-form");
  if (!(form instanceof HTMLFormElement)) {
    return;
  }

  const kindSelect = form.querySelector("#source-kind");
  if (!(kindSelect instanceof HTMLSelectElement)) {
    return;
  }

  updateConnectorFields(form);
  kindSelect.addEventListener("change", () => updateConnectorFields(form));
}

initializeSourceForm();
