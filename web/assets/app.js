(() => {
  'use strict';

  const $ = (sel, root = document) => root.querySelector(sel);
  const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));

  const faNF = new Intl.NumberFormat('fa-IR');
  const fa = (n) => faNF.format(Number(n) || 0);
  const faDigits = (value) => String(value).replace(/[0-9]/g, (d) => '۰۱۲۳۴۵۶۷۸۹'[Number(d)]);
  const money = (n) => `${fa(n)} تومان`;
  const feeFor = (subtotal) => Math.round((subtotal * 0.05) / 1000) * 1000;

  const el = (tag, className, text) => {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  };

  const svgIcon = (id, className = 'icon') => {
    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    svg.setAttribute('class', className);
    svg.setAttribute('aria-hidden', 'true');
    const use = document.createElementNS('http://www.w3.org/2000/svg', 'use');
    use.setAttribute('href', `#${id}`);
    svg.append(use);
    return svg;
  };

  const clamp = (n, min, max) => Math.min(max, Math.max(min, n));

  async function api(path, options = {}) {
    const init = { method: options.method || 'GET', headers: {} };
    if (options.body !== undefined) {
      init.headers['Content-Type'] = 'application/json';
      init.body = JSON.stringify(options.body);
    }
    let response;
    try {
      response = await fetch(path, init);
    } catch {
      const err = new Error('ارتباط با سرور برقرار نشد. اتصال خود را بررسی کنید.');
      err.code = 'network';
      throw err;
    }
    let payload = null;
    try {
      payload = await response.json();
    } catch {
      payload = null;
    }
    if (!response.ok || !payload || payload.ok === false) {
      const info = payload && payload.error ? payload.error : {};
      const err = new Error(info.message || 'درخواست انجام نشد. دوباره تلاش کنید.');
      err.code = info.code || 'http_' + response.status;
      err.seats = info.seats || [];
      err.status = response.status;
      throw err;
    }
    return payload;
  }

  const state = {
    data: null,
    selected: new Set(),
    locked: false,
    holdDeadline: 0,
    holdWindow: 0,
    holdID: null,
    expiryHandled: false,
    timer: null,
    busy: false,
    paying: false,
    ticket: null,
    payOpen: false,
    lastTotal: 0,
  };

  const dom = {};

  document.addEventListener('DOMContentLoaded', init);

  async function init() {
    cacheDom();
    bindEvents();
    await refresh();
    connectStream();
  }

  function cacheDom() {
    dom.seatMap = $('#seatMap');
    dom.summaryBody = $('#summaryBody');
    dom.summaryTotal = $('#summaryTotal');
    dom.seatCount = $('#seatCount');
    dom.subtotal = $('#subtotal');
    dom.fee = $('#fee');
    dom.total = $('#total');
    dom.primary = $('#primaryAction');
    dom.freeCount = $('#freeCount');
    dom.liveStatus = $('#liveStatus');
    dom.stepper = $('#stepper');
    dom.mobileBar = $('#mobileBar');
    dom.mobileCount = $('#mobileCount');
    dom.mobileTotal = $('#mobileTotal');
    dom.mobileAction = $('#mobileAction');
    dom.toastStack = $('#toastStack');
    dom.seatTip = $('#seatTip');
    dom.payModal = $('#payModal');
    dom.payForm = $('#payForm');
    dom.payAmount = $('#payAmount');
    dom.payBtnAmount = $('#payBtnAmount');
    dom.paySubmit = $('#paySubmit');
    dom.payError = $('#payError');
    dom.cardNumber = $('#cardNumber');
    dom.expiry = $('#expiry');
    dom.cvv = $('#cvv');
    dom.cardHolder = $('#cardHolder');
    dom.forceFail = $('#forceFail');
    dom.ticketLayer = $('#ticketLayer');
    dom.nav = $('#nav');
    dom.menuBtn = $('#menuBtn');
  }

  function bindEvents() {
    dom.seatMap.addEventListener('click', onSeatClick);
    dom.seatMap.addEventListener('pointerover', onSeatHover);
    dom.seatMap.addEventListener('pointerout', hideTip);
    dom.seatMap.addEventListener('focusin', onSeatHover);
    dom.seatMap.addEventListener('focusout', hideTip);

    dom.summaryBody.addEventListener('click', (event) => {
      const remove = event.target.closest('.remove-seat');
      if (remove) {
        state.selected.delete(remove.dataset.id);
        renderSeats();
        renderSummary();
        return;
      }
      if (event.target.closest('.edit-hold')) releaseHold();
    });

    dom.primary.addEventListener('click', onPrimaryAction);
    dom.mobileAction.addEventListener('click', onPrimaryAction);

    $('#shareBtn').addEventListener('click', shareEvent);
    $('#loginBtn').addEventListener('click', () => toast('ورود با شماره موبایل به‌زودی فعال می‌شود.', 'info'));

    $('#closePay').addEventListener('click', () => closePayModal());
    $('#cancelPay').addEventListener('click', () => closePayModal());
    dom.payModal.addEventListener('click', (event) => {
      if (event.target === dom.payModal) closePayModal();
    });
    dom.payForm.addEventListener('submit', submitPayment);

    dom.cardNumber.addEventListener('input', () => {
      const digits = dom.cardNumber.value.replace(/\D/g, '').slice(0, 16);
      dom.cardNumber.value = digits.replace(/(\d{4})(?=\d)/g, '$1 ');
    });
    dom.expiry.addEventListener('input', () => {
      let digits = dom.expiry.value.replace(/\D/g, '').slice(0, 4);
      dom.expiry.value = digits.length > 2 ? `${digits.slice(0, 2)}/${digits.slice(2)}` : digits;
    });
    dom.cvv.addEventListener('input', () => {
      dom.cvv.value = dom.cvv.value.replace(/\D/g, '').slice(0, 4);
    });

    $('#printTicket').addEventListener('click', () => window.print());
    $('#newBooking').addEventListener('click', () => {
      closeTicket(true);
      $('.hall-panel').scrollIntoView({ behavior: 'smooth', block: 'start' });
    });
    $('#closeTicket').addEventListener('click', () => closeTicket(true));

    dom.menuBtn.addEventListener('click', () => {
      const open = dom.nav.classList.toggle('is-open');
      dom.menuBtn.setAttribute('aria-expanded', String(open));
    });
    dom.nav.addEventListener('click', (event) => {
      if (event.target.closest('a')) {
        dom.nav.classList.remove('is-open');
        dom.menuBtn.setAttribute('aria-expanded', 'false');
      }
    });

    document.addEventListener('keydown', (event) => {
      if (event.key !== 'Escape') return;
      if (!dom.payModal.hidden && !state.paying) closePayModal();
      dom.nav.classList.remove('is-open');
      dom.menuBtn.setAttribute('aria-expanded', 'false');
    });
    window.addEventListener('scroll', hideTip, { passive: true });
  }

  async function refresh() {
    try {
      const payload = await api('/api/state');
      applyState(payload.state);
    } catch (err) {
      toast(err.message, 'error');
    }
  }

  function applyState(next, options = {}) {
    if (!next) return;
    const previous = state.data;
    const previousHold = previous && previous.hold ? previous.hold : null;

    if (next.hold) {
      if (next.hold.id !== state.holdID) {
        state.expiryHandled = false;
        state.holdID = next.hold.id;
      }
      state.locked = true;
      state.selected = new Set(next.hold.seatIds);
      state.holdWindow = Math.max(1, new Date(next.hold.expiresAt).getTime() - new Date(next.serverTime).getTime());
      state.holdDeadline = Date.now() + state.holdWindow;
    } else {
      const cameFromHold = Boolean(previousHold);
      state.locked = false;
      state.holdID = null;
      state.holdDeadline = 0;
      state.holdWindow = 0;

      if (state.payOpen && !state.paying && cameFromHold) {
        closePayModal(true);
        toast('زمان نگه‌داری صندلی‌ها تمام شد. دوباره انتخاب کنید.', 'error');
      }

      const available = new Map(next.seats.filter((seat) => seat.status === 'available').map((seat) => [seat.id, seat]));
      const lost = [];
      for (const id of Array.from(state.selected)) {
        if (!available.has(id)) {
          state.selected.delete(id);
          lost.push(id);
        }
      }
      if (lost.length && !options.silent) {
        toast(`صندلی ${lost.join('، ')} دیگر آزاد نیست و از انتخاب شما حذف شد.`, 'error');
      }
    }

    state.data = next;
    render();
  }

  function render() {
    if (!state.data) return;
    renderSeats();
    renderSummary();
    renderStats();
    updateStepper();
    updateMobileBar();
  }

  function seatById(id) {
    return state.data ? state.data.seats.find((seat) => seat.id === id) : null;
  }

  function renderSeats() {
    if (!state.data) return;
    hideTip();
    const focused = document.activeElement && document.activeElement.classList.contains('seat')
      ? document.activeElement.dataset.id
      : null;

    const rows = state.data.rows;
    const byRow = new Map(rows.map((row) => [row, []]));
    for (const seat of state.data.seats) {
      if (byRow.has(seat.row)) byRow.get(seat.row).push(seat);
    }

    const fragment = document.createDocumentFragment();
    for (const row of rows) {
      const rowEl = el('div', 'seat-row');
      const leftLabel = el('span', 'row-label', row);
      if (row === 'A' || row === 'B') leftLabel.classList.add('is-premium');
      const seatsWrap = el('div', 'row-seats');

      for (const seat of byRow.get(row)) {
        if (seat.number === 5 || seat.number === 9) {
          const aisle = el('span', 'aisle');
          aisle.setAttribute('aria-hidden', 'true');
          seatsWrap.append(aisle);
        }

        const button = document.createElement('button');
        button.type = 'button';
        button.className = 'seat';
        button.dataset.id = seat.id;
        button.dataset.price = String(seat.price);
        button.dataset.tier = seat.tier;
        button.dataset.status = seat.status;
        button.textContent = fa(seat.number);

        const mine = seat.mine || state.selected.has(seat.id);
        if (mine) {
          button.classList.add('is-selected');
          button.dataset.mine = 'true';
        } else if (seat.status === 'booked') {
          button.classList.add('is-booked');
          button.disabled = true;
        } else if (seat.status === 'held') {
          button.classList.add('is-held');
          button.disabled = true;
        }

        if (state.locked && !mine) {
          button.setAttribute('aria-disabled', 'true');
        }

        const statusLabel = mine ? 'انتخاب شما' : seat.status === 'booked' ? 'فروخته‌شده' : seat.status === 'held' ? 'در حال خرید' : 'آزاد';
        button.setAttribute('aria-pressed', mine ? 'true' : 'false');
        button.setAttribute('aria-label', `صندلی ${seat.id}، ردیف ${seat.row}، ${seat.tier}، ${statusLabel}، ${money(seat.price)}`);
        button.title = `${seat.id} · ${seat.tier} · ${money(seat.price)}`;
        seatsWrap.append(button);
      }

      const rightLabel = el('span', 'row-label', row);
      if (row === 'A' || row === 'B') rightLabel.classList.add('is-premium');
      rowEl.append(leftLabel, seatsWrap, rightLabel);
      fragment.append(rowEl);
    }

    dom.seatMap.replaceChildren(fragment);

    if (focused) {
      const restored = dom.seatMap.querySelector(`.seat[data-id="${focused}"]`);
      if (restored) restored.focus({ preventScroll: true });
    }
  }

  function onSeatClick(event) {
    const button = event.target.closest('.seat');
    if (!button) return;
    if (state.locked) {
      toast('صندلی‌ها برای شما نگه داشته شده‌اند. برای تغییر، «ویرایش انتخاب» را بزنید.', 'info');
      return;
    }
    if (button.disabled) return;

    const id = button.dataset.id;
    if (state.selected.has(id)) {
      state.selected.delete(id);
      button.classList.remove('is-selected');
      button.classList.add('is-available');
      button.setAttribute('aria-pressed', 'false');
    } else {
      if (state.selected.size >= 8) {
        toast('حداکثر ۸ صندلی در هر رزرو قابل انتخاب است.', 'error');
        return;
      }
      state.selected.add(id);
      button.classList.add('is-selected');
      button.setAttribute('aria-pressed', 'true');
    }
    hideTip();
    renderSummary();
    updateMobileBar();
  }

  function onSeatHover(event) {
    const button = event.target.closest('.seat');
    if (!button) return;
    showTip(button);
  }

  function showTip(button) {
    const seat = seatById(button.dataset.id);
    if (!seat) return;
    const mine = button.dataset.mine === 'true' || state.selected.has(seat.id);
    const status = mine ? 'انتخاب شما' : seat.status === 'booked' ? 'فروخته شده' : seat.status === 'held' ? 'در حال خرید توسط کاربر دیگر' : 'آزاد';

    dom.seatTip.replaceChildren();
    const title = el('strong', '', `${seat.id} · ${seat.tier}`);
    const statusEl = el('span', mine ? 'tip-status' : '', status);
    if (seat.status === 'booked') statusEl.className = 'tip-status';
    const price = el('span', 'tip-price', money(seat.price));
    dom.seatTip.append(title, statusEl, price);
    dom.seatTip.hidden = false;

    const rect = button.getBoundingClientRect();
    const tipRect = dom.seatTip.getBoundingClientRect();
    let left = rect.left + rect.width / 2 - tipRect.width / 2;
    left = Math.max(8, Math.min(window.innerWidth - tipRect.width - 8, left));
    let top = rect.top - tipRect.height - 8;
    if (top < 8) top = rect.bottom + 8;
    dom.seatTip.style.left = `${Math.round(left)}px`;
    dom.seatTip.style.top = `${Math.round(top)}px`;
  }

  function hideTip() {
    if (dom.seatTip) dom.seatTip.hidden = true;
  }

  function selectedSeats() {
    if (!state.data) return [];
    return Array.from(state.selected)
      .map((id) => state.data.seats.find((seat) => seat.id === id))
      .filter(Boolean);
  }

  function renderSummary() {
    if (!state.data) return;
    const chosen = selectedSeats();
    const hold = state.data.hold || null;

    dom.seatCount.textContent = `${fa(chosen.length)} صندلی`;
    dom.summaryBody.replaceChildren();

    if (hold) {
      const banner = el('div', 'hold-banner');
      const top = el('div', 'hold-top');
      const countdown = el('span', 'countdown', '۰۵:۰۰');
      countdown.id = 'countdown';
      top.append(el('span', 'hold-label', 'رزرو موقت شما'), countdown);
      banner.append(top, el('p', '', 'صندلی‌ها تا پایان شمارش برای شما نگه داشته شده‌اند.'));
      const track = el('div', 'hold-track');
      track.append(el('span'));
      banner.append(track);
      dom.summaryBody.append(banner);
    }

    if (!chosen.length) {
      const empty = el('div', 'empty-state');
      empty.append(svgIcon('i-seat'));
      empty.append(el('p', '', 'هنوز صندلی‌ای انتخاب نشده'));
      empty.append(el('span', '', 'روی صندلی‌های آزاد نقشه بزنید تا سفارش شما ساخته شود.'));
      dom.summaryBody.append(empty);
      dom.summaryTotal.hidden = true;
      dom.primary.disabled = true;
      dom.primary.textContent = 'انتخاب صندلی';
      return;
    }

    const list = el('div', 'selected-list');
    for (const seat of chosen) {
      const row = el('div', 'sel-seat');
      row.append(el('span', 'seat-tag', seat.id));
      const meta = el('div', 'sel-meta');
      meta.append(el('strong', '', `ردیف ${seat.row} · صندلی ${fa(seat.number)}`));
      meta.append(el('span', '', `${seat.tier} · ${money(seat.price)}`));
      row.append(meta);
      if (hold) {
        const lock = el('span', 'lock-mark');
        lock.append(svgIcon('i-lock'));
        row.append(lock);
      } else {
        const remove = el('button', 'remove-seat');
        remove.type = 'button';
        remove.dataset.id = seat.id;
        remove.setAttribute('aria-label', `حذف صندلی ${seat.id}`);
        remove.append(svgIcon('i-x'));
        row.append(remove);
      }
      list.append(row);
    }
    dom.summaryBody.append(list);

    if (hold) {
      const edit = el('button', 'edit-hold', 'ویرایش انتخاب و آزادسازی صندلی‌ها');
      edit.type = 'button';
      dom.summaryBody.append(edit);
    }

    const subtotal = hold ? hold.subtotal : chosen.reduce((sum, seat) => sum + seat.price, 0);
    const fee = hold ? hold.fee : feeFor(subtotal);
    const total = hold ? hold.total : subtotal + fee;

    dom.subtotal.textContent = money(subtotal);
    dom.fee.textContent = money(fee);
    dom.total.textContent = money(total);
    dom.summaryTotal.hidden = false;

    dom.primary.disabled = state.busy;
    dom.primary.textContent = hold ? 'پرداخت و دریافت بلیت' : 'ادامه و پرداخت';

    if (hold) startCountdown();
    else stopCountdown();
  }

  function startCountdown() {
    stopCountdown();
    if (!state.data || !state.data.hold) return;
    const tick = () => {
      if (!state.data || !state.data.hold) {
        stopCountdown();
        return;
      }
      const remaining = state.holdDeadline - Date.now();
      const countdown = $('#countdown');
      const bar = $('.hold-track span');
      const seconds = Math.max(0, Math.ceil(remaining / 1000));
      const mm = String(Math.floor(seconds / 60)).padStart(2, '0');
      const ss = String(seconds % 60).padStart(2, '0');
      if (countdown) countdown.textContent = faDigits(`${mm}:${ss}`);
      if (bar) bar.style.width = `${clamp((remaining / state.holdWindow) * 100, 0, 100)}%`;

      if (remaining <= 0 && !state.expiryHandled) {
        state.expiryHandled = true;
        state.selected.clear();
        stopCountdown();
        toast('زمان نگه‌داری صندلی‌ها تمام شد. صندلی‌ها آزاد شدند.', 'error');
        setTimeout(refresh, 700);
      }
    };
    tick();
    state.timer = setInterval(tick, 1000);
  }

  function stopCountdown() {
    if (state.timer) {
      clearInterval(state.timer);
      state.timer = null;
    }
  }

  async function onPrimaryAction() {
    if (state.busy) return;
    if (state.data && state.data.hold) {
      openPayModal();
      return;
    }
    if (!state.selected.size) {
      toast('ابتدا از نقشه سالن صندلی انتخاب کنید.', 'info');
      return;
    }
    await createHold();
  }

  async function createHold() {
    setBusy(true, 'در حال نگه‌داشتن صندلی‌ها…');
    try {
      const payload = await api('/api/hold', {
        method: 'POST',
        body: { seatIds: Array.from(state.selected) },
      });
      applyState(payload.state, { silent: true });
      toast('صندلی‌ها تا ۵ دقیقه برای شما نگه داشته شد.', 'success');
      if (window.innerWidth <= 980) {
        $('.summary-card').scrollIntoView({ behavior: 'smooth', block: 'center' });
      }
    } catch (err) {
      handleBookingError(err);
    } finally {
      setBusy(false);
      renderSummary();
      updateMobileBar();
    }
  }

  async function releaseHold() {
    if (!state.data || !state.data.hold || state.busy) return;
    const holdID = state.data.hold.id;
    setBusy(true, 'در حال آزادسازی…');
    try {
      const payload = await api('/api/release', { method: 'POST', body: { holdId: holdID } });
      applyState(payload.state, { silent: true });
      toast('رزرو موقت لغو شد. حالا می‌توانید انتخاب خود را تغییر دهید.', 'info');
    } catch (err) {
      handleBookingError(err);
    } finally {
      setBusy(false);
      renderSummary();
      updateMobileBar();
    }
  }

  function handleBookingError(err) {
    if (err.code === 'seat_taken') {
      const seats = Array.isArray(err.seats) ? err.seats : [];
      seats.forEach((id) => state.selected.delete(id));
      toast(err.message, 'error');
      refresh();
      return;
    }
    if (err.code === 'hold_exists' || err.code === 'hold_not_found' || err.code === 'hold_expired') {
      if (err.code === 'hold_not_found' || err.code === 'hold_expired') state.selected.clear();
      toast(err.message, 'error');
      refresh();
      return;
    }
    toast(err.message, 'error');
  }

  function setBusy(busy, label) {
    state.busy = busy;
    if (busy) {
      dom.primary.disabled = true;
      dom.primary.replaceChildren(el('span', 'spinner'), document.createTextNode(label || 'در حال انجام…'));
      dom.mobileAction.disabled = true;
      dom.mobileAction.replaceChildren(el('span', 'spinner'), document.createTextNode('صبر کنید'));
    } else {
      dom.primary.disabled = false;
      dom.mobileAction.disabled = false;
      dom.mobileAction.textContent = 'ادامه';
    }
  }

  function openPayModal() {
    const hold = state.data && state.data.hold;
    if (!hold) return;
    state.payOpen = true;
    state.lastTotal = hold.total;
    dom.payAmount.textContent = money(hold.total);
    dom.payBtnAmount.textContent = money(hold.total);
    dom.payError.hidden = true;
    dom.payError.textContent = '';
    dom.forceFail.checked = false;
    dom.payModal.hidden = false;
    document.body.classList.add('no-scroll');
    updateStepper();
    setTimeout(() => {
      dom.cardNumber.focus({ preventScroll: true });
    }, 60);
  }

  function closePayModal(force = false) {
    if (state.paying && !force) return;
    dom.payModal.hidden = true;
    state.payOpen = false;
    if (dom.ticketLayer.hidden) document.body.classList.remove('no-scroll');
    updateStepper();
  }

  function showPayError(message) {
    dom.payError.textContent = message;
    dom.payError.hidden = false;
  }

  async function submitPayment(event) {
    event.preventDefault();
    if (state.paying || !state.data || !state.data.hold) return;

    const digits = dom.cardNumber.value.replace(/\D/g, '');
    if (digits.length !== 16 || !luhn(digits)) {
      showPayError('شماره کارت معتبر نیست. برای تست از ۴۲۴۲ ۴۲۴۲ ۴۲۴۲ ۴۲۴۲ استفاده کنید.');
      dom.cardNumber.focus();
      return;
    }
    if (!/^\d{2}\/\d{2}$/.test(dom.expiry.value)) {
      showPayError('تاریخ انقضا را به شکل ماه/سال وارد کنید.');
      dom.expiry.focus();
      return;
    }
    if (!/^\d{3,4}$/.test(dom.cvv.value)) {
      showPayError('CVV2 باید ۳ یا ۴ رقم باشد.');
      dom.cvv.focus();
      return;
    }
    if (dom.cardHolder.value.trim().length < 2) {
      showPayError('نام دارنده کارت را وارد کنید.');
      dom.cardHolder.focus();
      return;
    }

    state.paying = true;
    state.lastTotal = state.data.hold.total;
    dom.paySubmit.disabled = true;
    dom.paySubmit.replaceChildren(el('span', 'spinner'), document.createTextNode('در حال اتصال به درگاه…'));
    dom.payError.hidden = true;

    try {
      const payload = await api('/api/pay', {
        method: 'POST',
        body: {
          holdId: state.data.hold.id,
          cardNumber: digits,
          cardHolder: dom.cardHolder.value.trim(),
          expiry: dom.expiry.value,
          cvv: dom.cvv.value,
          simulateFailure: dom.forceFail.checked,
        },
      });
      state.selected.clear();
      state.ticket = payload.booking;
      applyState(payload.state, { silent: true });
      closePayModal(true);
      showTicket(payload.booking);
      toast('پرداخت تأیید شد؛ بلیت شما صادر شد.', 'success');
    } catch (err) {
      showPayError(err.message);
      if (err.code === 'payment_failed') refresh();
      if (['hold_not_found', 'hold_expired', 'seat_taken', 'forbidden'].includes(err.code)) {
        toast(err.message, 'error');
        setTimeout(async () => {
          closePayModal(true);
          state.selected.clear();
          await refresh();
        }, 1400);
      }
    } finally {
      state.paying = false;
      dom.paySubmit.disabled = false;
      dom.paySubmit.replaceChildren(document.createTextNode('پرداخت '), el('span', '', money(state.lastTotal)));
    }
  }

  function luhn(number) {
    let sum = 0;
    let double = false;
    for (let i = number.length - 1; i >= 0; i -= 1) {
      let digit = Number(number[i]);
      if (double) {
        digit *= 2;
        if (digit > 9) digit -= 9;
      }
      sum += digit;
      double = !double;
    }
    return sum % 10 === 0;
  }

  function showTicket(booking) {
    $('#tkEvent').textContent = booking.event.title;
    $('#tkArtist').textContent = booking.event.artist;
    $('#tkDate').textContent = booking.event.dateLabel;
    $('#tkTime').textContent = booking.event.timeLabel;
    $('#tkVenue').textContent = `${booking.event.venue}، ${booking.event.city}`;
    $('#tkCode').textContent = booking.code;

    const seatsWrap = $('#tkSeats');
    seatsWrap.replaceChildren();
    booking.seats.forEach((seat) => {
      seatsWrap.append(el('span', 'ticket-seat', seat.id));
    });

    const firstSeat = booking.seats[0];
    $('#tkStubSeat').textContent = firstSeat ? firstSeat.id : '—';
    $('#tkStubDate').textContent = booking.seats.length > 1
      ? `${fa(booking.seats.length)} صندلی`
      : booking.event.dateLabel.replace('پنجشنبه ', '');
    $('#tkStubCode').textContent = `NO. ${booking.code.replace('SD-', '')}`;

    renderBarcode($('#tkBarcode'), booking.code);

    dom.ticketLayer.hidden = false;
    document.body.classList.add('no-scroll');
    updateStepper();
    $('#closeTicket').focus({ preventScroll: true });
  }

  function closeTicket(reset) {
    dom.ticketLayer.hidden = true;
    state.ticket = null;
    document.body.classList.remove('no-scroll');
    if (reset) {
      state.selected.clear();
      updateStepper();
      updateMobileBar();
      refresh();
    }
  }

  function renderBarcode(container, code) {
    const NS = 'http://www.w3.org/2000/svg';
    const svg = document.createElementNS(NS, 'svg');
    svg.setAttribute('viewBox', '0 0 148 31');
    svg.setAttribute('preserveAspectRatio', 'none');
    let seed = 7;
    for (let i = 0; i < code.length; i += 1) {
      seed = (seed * 31 + code.charCodeAt(i)) % 9973;
    }
    let x = 0;
    while (x < 147) {
      seed = (seed * 1103515245 + 12345) % 2147483647;
      const width = 1 + (seed % 3);
      const gap = 1 + ((seed >> 3) % 2);
      const rect = document.createElementNS(NS, 'rect');
      rect.setAttribute('x', String(x));
      rect.setAttribute('y', '0');
      rect.setAttribute('width', String(width));
      rect.setAttribute('height', '31');
      rect.setAttribute('fill', '#171512');
      svg.append(rect);
      x += width + gap;
    }
    container.replaceChildren(svg);
  }

  let eventSource = null;

  function connectStream() {
    if (!('EventSource' in window)) {
      setStreamState(false);
      return;
    }
    eventSource = new EventSource('/api/events');
    eventSource.onopen = () => setStreamState(true);
    eventSource.onerror = () => setStreamState(false);
    eventSource.onmessage = (event) => {
      try {
        const next = JSON.parse(event.data);
        applyState(next, { silent: true });
        setStreamState(true);
      } catch {
        setStreamState(false);
      }
    };
  }

  function setStreamState(online) {
    if (!dom.liveStatus) return;
    dom.liveStatus.classList.toggle('is-offline', !online);
    const label = $('#liveText');
    if (label) label.textContent = online ? 'اتصال زنده' : 'اتصال قطع شد';
  }

  function renderStats() {
    if (!state.data) return;
    dom.freeCount.textContent = fa(state.data.stats.available);
  }

  function updateStepper() {
    if (!dom.stepper) return;
    let active = 1;
    if (!dom.ticketLayer.hidden) active = 3;
    else if (state.data && state.data.hold) active = 2;
    $$('.step', dom.stepper).forEach((step) => {
      const number = Number(step.dataset.step);
      step.classList.toggle('is-active', number === active);
      step.classList.toggle('is-done', number < active);
    });
  }

  function updateMobileBar() {
    const count = selectedSeats().length;
    const hold = state.data && state.data.hold;
    const visible = count > 0 || Boolean(hold);
    dom.mobileBar.classList.toggle('is-visible', visible);
    document.body.classList.toggle('has-mobile-bar', visible);
    if (!visible) return;

    dom.mobileCount.textContent = `${fa(count)} صندلی`;
    if (hold) dom.mobileTotal.textContent = money(hold.total);
    else {
      const subtotal = selectedSeats().reduce((sum, seat) => sum + seat.price, 0);
      dom.mobileTotal.textContent = money(subtotal + feeFor(subtotal));
    }
    dom.mobileAction.textContent = hold ? 'پرداخت' : 'ادامه';
  }

  function toast(message, type = 'info') {
    const node = el('div', `toast toast-${type}`, message);
    dom.toastStack.append(node);
    while (dom.toastStack.children.length > 3) dom.toastStack.firstElementChild.remove();
    setTimeout(() => {
      node.style.opacity = '0';
      node.style.transform = 'translateY(6px)';
      node.style.transition = 'opacity .2s ease, transform .2s ease';
      setTimeout(() => node.remove(), 220);
    }, 4600);
  }

  async function shareEvent() {
    const url = window.location.href;
    try {
      await navigator.clipboard.writeText(url);
      toast('لینک رویداد در کلیپ‌بورد کپی شد.', 'success');
    } catch {
      toast('کپی خودکار ممکن نشد؛ آدرس صفحه را از نوار مرورگر بردارید.', 'error');
    }
  }
})();
