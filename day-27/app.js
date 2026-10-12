'use strict';
const $ = id => document.getElementById(id);
let history = [], pending = false, ready = false, generation = 0, controller;
function status(text, error = false) { $('status').textContent = text; $('status').classList.toggle('error', error); }
function controls() { $('send').disabled = !ready || pending || history.length >= 16; $('prompt').disabled = pending; }
function bubble(role, text, detail = '') {
  const item = document.createElement('article'); item.className = 'panel ' + role;
  const title = document.createElement('h2'); title.textContent = role === 'user' ? 'Вы' : 'Qwen · локальный ответ';
  const body = document.createElement('p'); body.className = 'content'; body.textContent = text;
  item.append(title, body);
  if (detail) { const note = document.createElement('p'); note.className = 'note'; note.textContent = detail; item.append(note); }
  $('empty').hidden = true; $('conversation').append(item); return item;
}
$('reset').addEventListener('click', () => {
  generation++; controller?.abort(); pending = false; history = [];
  $('conversation').querySelectorAll('article').forEach(e => e.remove());
  $('empty').hidden = false; $('prompt').value = ''; $('trace').textContent = 'Пока запросов нет.';
  status('Новый диалог.'); controls(); $('prompt').focus();
});
$('composer').addEventListener('submit', async event => {
  event.preventDefault(); const content = $('prompt').value.trim();
  if (pending || !ready || !content || history.length >= 16) return;
  const token = generation, messages = [...history, {role:'user', content}];
  const user = bubble('user', content); pending = true; controller = new AbortController(); controls();
  status('Qwen генерирует ответ… Первый запрос может включать загрузку модели.');
  try {
    const res = await fetch('/api/chat', {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify({messages}), signal:controller.signal});
    const data = await res.json(); if (token !== generation) return;
    if (!res.ok) throw new Error(data.error || 'Ошибка HTTP ' + res.status);
    const reply = data.response.message.content;
    history = [...messages, {role:'assistant', content:reply}];
    bubble('assistant', reply, `${(data.wall_ms/1000).toFixed(2)} с · ${data.response.eval_count} выходных токенов · ${history.length/2}/8 ответов`);
    $('trace').textContent = JSON.stringify(data, null, 2); $('prompt').value = '';
    status(history.length >= 16 ? 'Лимит диалога достигнут. Нажмите «Новый чат».' : 'Ответ получен от локальной модели.');
  } catch (error) {
    if (token !== generation) return;
    user.remove(); $('empty').hidden = history.length > 0;
    status(error.message.replace(/[.!?…]*\s*$/, '') + '. Запрос сохранён в поле; можно повторить.', true);
  } finally { if (token === generation) { pending = false; controls(); } }
});
async function init() {
  try {
    const res = await fetch('/api/config'); const data = await res.json();
    if (!res.ok) throw new Error(data.error || 'Модель недоступна');
    $('identity').textContent = `${data.model} · ${data.endpoint} · digest ${data.digest}`;
    ready = true; status('Готово. Напишите запрос.');
  } catch (error) { status(error.message.replace(/[.!?…]*\s*$/, '') + '. Проверьте Ollama и перезагрузите страницу.', true); }
  controls();
}
init();
