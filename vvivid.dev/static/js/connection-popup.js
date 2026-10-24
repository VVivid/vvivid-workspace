// Professional connection popup for vvivid.dev
// Shows once per browser session for new visitors

(function() {
  'use strict';
  
  const POPUP_KEY = 'vvivid_connection_popup_shown';
  
  // Check if popup should be shown (once per session)
  function shouldShowPopup() {
    return !sessionStorage.getItem(POPUP_KEY);
  }
  
  // Mark popup as shown for this session
  function markPopupShown() {
    sessionStorage.setItem(POPUP_KEY, 'true');
  }
  
  // Create the popup HTML structure
  function createPopupHTML() {
    return `
      <div id="connection-popup-overlay" class="connection-popup-overlay">
        <div class="connection-popup">
          <button class="connection-popup-close" aria-label="Close popup" title="Close">
            <svg width="20" height="20" viewBox="0 0 20 20" fill="currentColor">
              <path fill-rule="evenodd" d="M4.293 4.293a1 1 0 011.414 0L10 8.586l4.293-4.293a1 1 0 111.414 1.414L11.414 10l4.293 4.293a1 1 0 01-1.414 1.414L10 11.414l-4.293 4.293a1 1 0 01-1.414-1.414L8.586 10 4.293 5.707a1 1 0 010-1.414z" clip-rule="evenodd"/>
            </svg>
          </button>
          
          <div class="connection-popup-content">
            <h3>👋 Hello!</h3>
            <p>I really appreciate you checking out my site. I'm always down to chat about tech, exchange ideas, or just connect with fellow engineers.</p>
            <p>If you're into backend stuff, Go, or cloud services – or honestly, if you just want to say hi – I'd love to hear from you!</p>
            
            <div class="connection-popup-links">
              <a href="mailto:oudavid@vvivid.dev" class="connection-popup-btn primary">
                <svg width="18" height="18" viewBox="0 0 24 24" fill="currentColor">
                  <path d="M20 4H4c-1.1 0-1.99.9-1.99 2L2 18c0 1.1.9 2 2 2h16c1.1 0 2-.9 2-2V6c0-1.1-.9-2-2-2zm0 4l-8 5-8-5V6l8 5 8-5v2z"/>
                </svg>
                Drop me a line
              </a>
              
              <a href="https://linkedin.com/in/oudavid-vvivid" target="_blank" rel="noopener noreferrer" class="connection-popup-btn secondary">
                <svg width="18" height="18" viewBox="0 0 24 24" fill="currentColor">
                  <path d="M20.447 20.452h-3.554v-5.569c0-1.328-.027-3.037-1.852-3.037-1.853 0-2.136 1.445-2.136 2.939v5.667H9.351V9h3.414v1.561h.046c.477-.9 1.637-1.85 3.37-1.85 3.601 0 4.267 2.37 4.267 5.455v6.286zM5.337 7.433c-1.144 0-2.063-.926-2.063-2.065 0-1.138.92-2.063 2.063-2.063 1.14 0 2.064.925 2.064 2.063 0 1.139-.925 2.065-2.064 2.065zm1.782 13.019H3.555V9h3.564v11.452zM22.225 0H1.771C.792 0 0 .774 0 1.729v20.542C0 23.227.792 24 1.771 24h20.451C23.2 24 24 23.227 24 22.271V1.729C24 .774 23.2 0 22.222 0h.003z"/>
                </svg>
                Connect on LinkedIn
              </a>
            </div>
            
          </div>
        </div>
      </div>
    `;
  }
  
  // Handle popup close
  function closePopup() {
    const popup = document.getElementById('connection-popup-overlay');
    if (popup) {
      popup.style.animation = 'connectionPopupFadeOut 0.3s ease-in-out forwards';
      setTimeout(() => {
        popup.remove();
      }, 300);
    }
    markPopupShown();
  }
  
  // Show the popup with animation
  function showPopup() {
    const popupHTML = createPopupHTML();
    document.body.insertAdjacentHTML('beforeend', popupHTML);
    
    const popup = document.getElementById('connection-popup-overlay');
    const closeBtn = popup.querySelector('.connection-popup-close');
    
    // Close button click
    closeBtn.addEventListener('click', closePopup);
    
    // Close on overlay click (outside popup)
    popup.addEventListener('click', function(e) {
      if (e.target === popup) {
        closePopup();
      }
    });
    
    // Close on Escape key
    document.addEventListener('keydown', function(e) {
      if (e.key === 'Escape' && document.getElementById('connection-popup-overlay')) {
        closePopup();
      }
    });
    
    // Track clicks for analytics (if needed)
    popup.querySelectorAll('a').forEach(link => {
      link.addEventListener('click', function() {
        // Could add analytics tracking here if desired
        markPopupShown();
      });
    });
  }
  
  // Initialize popup after DOM is loaded
  function init() {
    // Wait a bit after page load for better UX
    setTimeout(() => {
      if (shouldShowPopup()) {
        showPopup();
      }
    }, 2000); // Show after 2 seconds
  }
  
  // Start when DOM is ready
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
  
})();