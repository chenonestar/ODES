// 归档页的两个小动作。抽成文件而非内联，是为了让 CSP 有收紧的余地。
function odesDownloadArchive(btn) {
  var p = document.getElementById('apass').value;
  if (p.length < 10) { alert('归档口令至少 10 位，且须含两类以上字符'); return; }
  location.href = '/admin/projects/' + btn.dataset.proj +
    '/export?kind=archive&pass=' + encodeURIComponent(p);
}
function odesCopyPass() {
  document.getElementById('apass2').value = document.getElementById('apass').value;
}
