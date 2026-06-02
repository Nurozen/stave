FROM alpine:3.20

ARG PUBLIC_KEY

RUN apk add --no-cache openssh-server rsync \
    && adduser -D -h /home/stave stave \
    && echo 'stave:stave' | chpasswd \
    && mkdir -p /run/sshd /home/stave/.ssh \
    && ssh-keygen -A \
    && printf '%s\n' "$PUBLIC_KEY" > /home/stave/.ssh/authorized_keys \
    && chown -R stave:stave /home/stave/.ssh \
    && chmod 700 /home/stave/.ssh \
    && chmod 600 /home/stave/.ssh/authorized_keys

EXPOSE 22

CMD ["/usr/sbin/sshd", "-D", "-e"]
