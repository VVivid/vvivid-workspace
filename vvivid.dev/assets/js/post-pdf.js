(function () {
  function cleanText(value) {
    return (value || "").replace(/\s+/g, " ").trim().slice(0, 160);
  }

  function trackEvent(name, data) {
    if (!window.umami || typeof window.umami.track !== "function") {
      return;
    }

    try {
      window.umami.track(name, data || {});
    } catch (error) {
      // Analytics must never interfere with navigation or printing.
    }
  }

  function getLinkData(link) {
    var rawHref = link.getAttribute("href") || "";
    var url = new URL(rawHref, window.location.href);

    return {
      href: url.href,
      host: url.hostname || "",
      path: url.pathname || rawHref,
      label: cleanText(link.getAttribute("title") || link.getAttribute("aria-label") || link.textContent),
    };
  }

  function getProjectTitle(project) {
    var title = project.querySelector(".post-item-title");
    return cleanText(title ? title.textContent : "");
  }

  function classifyLink(link) {
    var rawHref = link.getAttribute("href") || "";

    if (!rawHref || rawHref.charAt(0) === "#") {
      return null;
    }

    var data = getLinkData(link);
    var project = link.closest(".project-item");
    var postCard = link.closest(".post-item");
    var footerSocial = link.closest(".footer_social-icons");
    var connectionPopup = link.closest(".connection-popup-links");
    var inContent = link.closest(".page-content");
    var isExternal = data.host && data.host !== window.location.hostname;

    if (connectionPopup) {
      return {
        name: "Connection popup click",
        data: {
          target: data.label || data.host || data.path,
          href: data.href,
        },
      };
    }

    if (footerSocial) {
      return {
        name: "Social link click",
        data: {
          network: cleanText(link.getAttribute("title") || data.host || data.path).toLowerCase(),
          href: data.href,
        },
      };
    }

    if (rawHref.indexOf("mailto:") === 0) {
      return {
        name: "Email link click",
        data: {
          label: data.label || "email",
          location: inContent ? "content" : "site",
        },
      };
    }

    if (project) {
      var isRepo = link.classList.contains("project-repo-link");
      var isThumbnail = link.classList.contains("project-thumbnail-link");

      return {
        name: isRepo ? "Project repo click" : "Project link click",
        data: {
          project: getProjectTitle(project),
          area: isRepo ? "repo" : isThumbnail ? "thumbnail" : "title",
          host: data.host,
          href: data.href,
        },
      };
    }

    if (postCard) {
      return {
        name: "Post card click",
        data: {
          post: data.label,
          path: data.path,
        },
      };
    }

    if (isExternal && inContent) {
      return {
        name: "External link click",
        data: {
          label: data.label || data.host,
          host: data.host,
          href: data.href,
        },
      };
    }

    return null;
  }

  function bindExportButtons() {
    document.querySelectorAll("[data-export-pdf]").forEach(function (button) {
      if (button.dataset.exportPdfBound === "true") {
        return;
      }

      button.dataset.exportPdfBound = "true";
      button.addEventListener("click", function () {
        var heading = document.querySelector("h1");

        trackEvent("PDF export", {
          path: window.location.pathname,
          title: cleanText(heading ? heading.textContent : document.title),
        });

        window.print();
      });
    });
  }

  function bindLinkTracking() {
    document.addEventListener("click", function (event) {
      var target = event.target;

      if (!target || typeof target.closest !== "function") {
        return;
      }

      var link = target.closest("a");
      if (!link) {
        return;
      }

      var tracked = classifyLink(link);
      if (tracked) {
        trackEvent(tracked.name, tracked.data);
      }
    }, true);
  }

  function init() {
    bindExportButtons();
    bindLinkTracking();
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})();
