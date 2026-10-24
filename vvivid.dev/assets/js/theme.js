(() => {
    "use strict";

    const forceDarkMode = () => {
        document.documentElement.classList.add("dark", "darkmode");
        document.documentElement.classList.remove("light");
        localStorage.setItem("theme", "dark");

        if (document.body) {
            document.body.dataset.theme = "dark";
            document.body.classList.remove("notransition");
        }
    };

    forceDarkMode();

    window.addEventListener("DOMContentLoaded", () => {
        forceDarkMode();

        const modeToggle = document.getElementById("mode");
        if (modeToggle) {
            modeToggle.setAttribute("aria-hidden", "true");
            modeToggle.style.display = "none";
            modeToggle.addEventListener("click", (event) => {
                event.preventDefault();
                forceDarkMode();
            });
        }

        const menuTrigger = document.getElementById("menu-trigger");
        if (menuTrigger) {
            menuTrigger.addEventListener("change", function () {
                const area = document.querySelector(".wrapper");
                if (!area) return;
                if (this.checked) {
                    area.classList.add("blurry");
                } else {
                    area.classList.remove("blurry");
                }
            });
        }
    });
})();
