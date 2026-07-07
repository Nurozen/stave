FROM alpine:3.20

ARG PUBLIC_KEY

# Test-only SSH host. Authentication is key-based via the injected PUBLIC_KEY;
# password auth is disabled so no credential is baked into the image.
RUN apk add --no-cache openssh-server rsync \
    && adduser -D -h /home/stave stave \
    && mkdir -p /run/sshd /home/stave/.ssh \
    && ssh-keygen -A \
    && printf '%s\n' "$PUBLIC_KEY" > /home/stave/.ssh/authorized_keys \
    && chown -R stave:stave /home/stave/.ssh \
    && chmod 700 /home/stave/.ssh \
    && chmod 600 /home/stave/.ssh/authorized_keys \
    && printf 'PasswordAuthentication no\nPermitRootLogin no\n' >> /etc/ssh/sshd_config

EXPOSE 22

CMD ["/usr/sbin/sshd", "-D", "-e"]
