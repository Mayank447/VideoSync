const BACKEND_URL = 'http://localhost:8080'; 

async function createSession() {
    try {
        const response = await fetch(`${BACKEND_URL}/api/sessions`, {
            method: 'POST'
        });
        
        if (!response.ok) throw new Error('Failed to create session');
        
        const { sessionKey, hostToken } = await response.json();
        const sessionKeyElement = document.getElementById('sessionKey');
        
        // Always display the session key
        sessionKeyElement.textContent = sessionKey;
        document.getElementById('sessionKeyContainer').style.display = 'block';

        // Clipboard handling with improved fallback
        try {
            if (navigator.clipboard && window.isSecureContext) {
                await navigator.clipboard.writeText(sessionKey);
                showTemporaryMessage('Session key copied to clipboard!', 'success');
            } else {
                // Fallback for insecure contexts/older browsers
                const textArea = document.createElement('textarea');
                textArea.value = sessionKey;
                textArea.style.position = 'fixed';
                textArea.style.left = '-9999px';
                document.body.appendChild(textArea);
                textArea.select();
                
                const success = document.execCommand('copy');
                document.body.removeChild(textArea);
                
                if (success) {
                    showTemporaryMessage('Session key copied!', 'success');
                } else {
                    showTemporaryMessage('Auto-copy failed. Please copy manually.', 'warning');
                    sessionKeyElement.focus();
                    sessionKeyElement.select();
                }
            }
        } catch (clipboardError) {
            console.warn('Clipboard error:', clipboardError);
            showTemporaryMessage('Copy failed. Please copy manually.', 'error');
            sessionKeyElement.focus();
            sessionKeyElement.select();
        }
        
        // Store host token in both session storage and URL
        sessionStorage.setItem('hostToken', hostToken);
        const newUrl = new URL(window.location.href);
        newUrl.searchParams.set('hostToken', hostToken);
        window.history.replaceState({}, '', newUrl);
        
    } catch (error) {
        console.error('Session creation error:', error);
        showTemporaryMessage('Error creating session. Please try again.', 'error');
    }
}

// Helper function for status messages
function showTemporaryMessage(message, type = 'info') {
    const statusDiv = document.getElementById('statusMessages');
    statusDiv.textContent = message;
    statusDiv.className = `status-${type}`;
    statusDiv.style.display = 'block';
    
    setTimeout(() => {
        statusDiv.style.display = 'none';
    }, 3000);
}

async function joinSession() {
    const sessionKey = document.getElementById('joinKey').value.trim();
    const errorElement = document.getElementById('errorMessage');
    
    if (!sessionKey) {
        showError('Please enter a session key');
        return;
    }

    try {
        console.log('Joining session:', sessionKey);
        
        // Get host token from session storage
        const hostToken = sessionStorage.getItem('hostToken');
        console.log('Host token from storage:', hostToken ? 'present' : 'not present');
        
        // Validate session with host token if available
        const validateUrl = `${BACKEND_URL}/api/sessions/${encodeURIComponent(sessionKey)}/validate`;
        const urlWithParams = hostToken ? 
            `${validateUrl}?hostToken=${encodeURIComponent(hostToken)}` : 
            validateUrl;
            
        console.log('Sending validation request to:', urlWithParams);
        
        const response = await fetch(urlWithParams);
        
        if (!response.ok) throw new Error('Invalid session key');
        
        const { valid } = await response.json();
        if (!valid) throw new Error('Invalid session key');
        
        window.location.href = `pages/player.html?sessionKey=${encodeURIComponent(sessionKey)}`;
        
    } catch (error) {
        showError('Invalid session key. Please check and try again.');
        console.error('Join error:', error);
    }
}

function showError(message) {
    const errorElement = document.getElementById('errorMessage');
    errorElement.textContent = message;
    errorElement.style.display = 'block';
    setTimeout(() => {
        errorElement.style.display = 'none';
    }, 3000);
}