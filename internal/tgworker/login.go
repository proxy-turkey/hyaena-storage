package tgworker

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
)

// loginIfNeeded, mevcut session yoksa interaktif login yapar.
func (s *Service) loginIfNeeded(ctx context.Context) error {
	status, err := s.client.Auth().Status(ctx)
	if err != nil {
		// oturum yok / bozuk → login akışı
	} else if status.Authorized {
		return nil // session zaten geçerli
	}

	phone := prompt("Telefon numarası (+90...): ")
	return s.loginWithPhone(ctx, phone, s.s.TwoFA)
}

// loginWithPhone, telefon + (isteğe bağlı) kod/password ile login yapar.
// code/password boşsa interaktif prompt kullanır.
func (s *Service) loginWithPhone(ctx context.Context, phone, password string) error {
	codePrompt := func(ctx context.Context, sentCode *tg.AuthSentCode) (string, error) {
		if s.pendingCode != "" {
			c := s.pendingCode
			s.pendingCode = ""
			return c, nil
		}
		return prompt("Telegram kodu: "), nil
	}

	var flow auth.Flow
	if password != "" {
		flow = auth.NewFlow(
			auth.Constant(phone, password, auth.CodeAuthenticatorFunc(codePrompt)),
			auth.SendCodeOptions{},
		)
	} else {
		flow = auth.NewFlow(
			auth.CodeOnly(phone, auth.CodeAuthenticatorFunc(codePrompt)),
			auth.SendCodeOptions{},
		)
	}
	return flow.Run(ctx, s.client.Auth())
}

// LoginArgs, komut satırından verilen login bilgileri.
type LoginArgs struct {
	Phone    string
	Code     string
	Password string
}

// prompt, terminalden satır okur.
func prompt(label string) string {
	fmt.Print(label)
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	return strings.TrimSpace(line)
}

// LoginOnce, tek seferlik interaktif login yapar ve session dosyasını yazar.
// Servisi başlatmadan önce ayrı bir adım olarak çalıştırılır (./tgshare --login).
func (s *Service) LoginOnce(ctx context.Context, args LoginArgs) error {
	client := telegram.NewClient(s.s.TelegramAPIID, s.s.TelegramAPIHash, telegram.Options{
		SessionStorage: &session.FileStorage{Path: s.s.SessionFile},
		NoUpdates:      true,
	})
	s.client = client
	return client.Run(ctx, func(ctx context.Context) error {
		status, err := client.Auth().Status(ctx)
		if err == nil && status.Authorized {
			me, _ := client.Self(ctx)
			log.Printf("Zaten girişli: %s", me.Username)
			return nil
		}

		// --phone verildiyse onu kullan; yoksa prompt
		phone := args.Phone
		if phone == "" {
			phone = prompt("Telefon numarası (+90...): ")
		}
		s.pendingCode = args.Code
		password := args.Password
		if password == "" {
			password = s.s.TwoFA
		}
		if err := s.loginWithPhone(ctx, phone, password); err != nil {
			return err
		}
		me, _ := client.Self(ctx)
		if me != nil {
			log.Printf("Giriş başarılı: %s (@%s)", me.FirstName, me.Username)
		}
		return nil
	})
}
