const form = document.getElementById('ip-form');
const input = document.getElementById('ip-input');
const rows = document.getElementById('rows');

form.addEventListener('submit', async (e) => {
  e.preventDefault();
  const ip = input.value.trim();
  if (!ip) return;
  const res = await fetch('/api/targets', {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify({ip})});
  if (!res.ok) alert(await res.text());
  input.value = '';
  await load();
});

async function removeIp(ip){
  await fetch('/api/targets?ip='+encodeURIComponent(ip), {method:'DELETE'});
  await load();
}

async function load(){
  const res = await fetch('/api/targets');
  const data = await res.json();
  rows.innerHTML = data.map(t => `
    <tr>
      <td>${t.ip}</td>
      <td class="${t.isUp ? 'up' : 'down'}">${t.isUp ? 'UP' : 'DOWN'}</td>
      <td>${t.lastLatencyMs ?? 0}</td>
      <td>${(t.avgLatencyMs ?? 0).toFixed(1)}</td>
      <td>${t.sent}</td>
      <td>${t.received}</td>
      <td>${(t.lossPercent ?? 0).toFixed(1)}</td>
      <td>${t.lastChecked || '-'}</td>
      <td><button class="remove" onclick="removeIp('${t.ip}')">Удалить</button></td>
    </tr>
  `).join('');
}

setInterval(load, 1000);
load();
window.removeIp = removeIp;
