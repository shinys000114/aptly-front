(function () {
  document.querySelectorAll("[data-filter-table]").forEach((input) => {
    const table = document.querySelector(input.dataset.filterTable);
    if (!table) return;

    input.addEventListener("input", () => {
      const needle = input.value.trim().toLowerCase();
      table.querySelectorAll("tbody tr").forEach((row) => {
        const text = row.textContent.toLowerCase();
        row.hidden = needle !== "" && !text.includes(needle);
      });
    });
  });

  document.querySelectorAll("[data-check-all]").forEach((toggle) => {
    const table = document.querySelector(toggle.dataset.checkAll);
    if (!table) return;

    toggle.addEventListener("change", () => {
      table.querySelectorAll('tbody input[type="checkbox"]').forEach((checkbox) => {
        if (!checkbox.closest("tr").hidden) {
          checkbox.checked = toggle.checked;
        }
      });
    });
  });

  document.addEventListener("submit", (event) => {
    const submitter = event.submitter;
    if (!submitter || !submitter.classList.contains("danger")) return;

    const form = event.target;
    const selected = form.querySelectorAll('tbody input[type="checkbox"]:checked').length;
    if (form.querySelector("tbody") && selected === 0) {
      event.preventDefault();
      alert("Select at least one item.");
      return;
    }

    if (!confirm("Apply this destructive action?")) {
      event.preventDefault();
    }
  });
})();
