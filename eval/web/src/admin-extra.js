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

// ── 拖拽排序（FR-FRM-022 / FR-FRM-023）──────────────────────────────
//
// 用原生 HTML5 拖放，不引第三方库：管理端只在一台笔记本的桌面浏览器上
// 跑，原生 API 完全够用，而多一个前端依赖就多一份要 vendored 进仓库、
// 要跟着升级的东西（LLD 12.7.4「能用原生工具时不叠兼容层」）。
//
// 提交的是**完整的新顺序**而不是"上移一位"：拖拽产生的本来就是一个新
// 顺序，整体覆盖是幂等的，重发一次结果相同；而增量接口一旦丢包，顺序
// 就错乱且事后无从发现。
(function () {
  function rowsOf(tbody) {
    return Array.prototype.filter.call(tbody.children, function (tr) {
      return tr.dataset && tr.dataset.id;
    });
  }

  function wire(table) {
    var url = table.dataset.reorder;
    var tbody = table.querySelector('tbody');
    if (!url || !tbody) return;
    var dragging = null;

    tbody.addEventListener('dragstart', function (e) {
      var tr = e.target.closest('tr[data-id]');
      if (!tr) return;
      dragging = tr;
      tr.classList.add('opacity-40');
      e.dataTransfer.effectAllowed = 'move';
      // Firefox 不设 data 就不触发 drop
      e.dataTransfer.setData('text/plain', tr.dataset.id);
    });

    tbody.addEventListener('dragover', function (e) {
      if (!dragging) return;
      e.preventDefault();
      var tr = e.target.closest('tr[data-id]');
      if (!tr || tr === dragging) return;
      var box = tr.getBoundingClientRect();
      var after = (e.clientY - box.top) > box.height / 2;
      tbody.insertBefore(dragging, after ? tr.nextSibling : tr);
    });

    tbody.addEventListener('dragend', function () {
      if (!dragging) return;
      dragging.classList.remove('opacity-40');
      dragging = null;
      save();
    });

    function save() {
      var ids = rowsOf(tbody).map(function (tr) { return tr.dataset.id; });
      fetch(url, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(ids)
      }).then(function (r) { return r.json(); }).then(function (j) {
        if (j && j.ok) return;
        // 保存失败必须让人知道：页面上顺序已经变了，若服务端没存下，
        // 刷新后又变回去，管理员会以为是自己看花了眼。
        alert((j && j.msg) || '排序未能保存，请刷新页面后重试');
        location.reload();
      }).catch(function () {
        alert('排序未能保存，请刷新页面后重试');
        location.reload();
      });
    }
  }

  document.addEventListener('DOMContentLoaded', function () {
    var tables = document.querySelectorAll('table[data-reorder]');
    Array.prototype.forEach.call(tables, wire);
  });
})();
